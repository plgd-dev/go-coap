package client

import (
	"context"
	"errors"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"net"
	"strings"
	"time"
)

type qblockCapability uint8

const (
	qblockCapabilityUnknown qblockCapability = iota
	qblockCapabilitySupported
	qblockCapabilityUnsupported
)

type qblockProbeGeneration struct {
	path          string
	context       context.Context
	cancel        context.CancelFunc
	deadline      time.Time
	waiters       uint32
	terminal      bool
	result        qblockCapabilityResult
	done, cleaned chan struct{}
}

func (cc *Conn) qblockCapabilityState() qblockCapability {
	cc.qblockProbeMu.Lock()
	defer cc.qblockProbeMu.Unlock()
	if cc.Context().Err() != nil {
		cc.qblockKnowledge = qblockCapabilityUnknown
	}
	return cc.qblockKnowledge
}

// ProbeQBlock performs a fresh explicit discovery. Pending calls with the same
// exact effective path share a connection-owned exchange; cancellation is local
// to each caller. Probing records session knowledge but never enables payload Q.
func (cc *Conn) ProbeQBlock(ctx context.Context, path string) (bool, error) {
	if err := cc.InitializationError(); err != nil {
		return false, err
	}
	if c := cc.qblockClient; c != nil && c.initErr != nil {
		return false, c.initErr
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := cc.Context().Err(); err != nil {
		return false, err
	}
	if path == "" {
		path = "/.well-known/core"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") || uint64(len(path)) > uint64(cc.qblockProbeLimit) {
		return false, errors.New("invalid q-block probe resource path")
	}
	if peer, ok := cc.RemoteAddr().(*net.UDPAddr); ok && peer != nil && peer.IP.IsMulticast() {
		return false, errors.New("q-block capability probe requires a unicast peer")
	}
	cc.qblockProbeMu.Lock()
	g := cc.qblockGeneration
	if g != nil {
		if g.terminal || g.path != path {
			cc.qblockProbeMu.Unlock()
			return false, ErrQBlockProbeInProgress
		}
		limit := cc.qblockMaxProbeWaiters
		if limit == 0 {
			limit = 64
		}
		if g.waiters >= limit {
			cc.qblockProbeMu.Unlock()
			return false, qblock.ErrLimitExceeded
		}
		g.waiters++
	} else {
		exchange, cancel := context.WithTimeout(cc.Context(), ExchangeLifetime)
		deadline, _ := exchange.Deadline()
		g = &qblockProbeGeneration{path: path, context: exchange, cancel: cancel, deadline: deadline, waiters: 1, done: make(chan struct{}), cleaned: make(chan struct{})}
		cc.qblockGeneration = g
		go cc.runQBlockProbe(g)
	}
	cc.qblockProbeMu.Unlock()
	select {
	case <-ctx.Done():
	case <-g.done:
	}
	cc.qblockProbeMu.Lock()
	g.waiters--
	last := g.waiters == 0
	if last && !g.terminal {
		cc.freezeQBlockProbeLocked(g, false, ctx.Err(), qblockCapabilityUnknown)
	}
	result := g.result
	cc.qblockProbeMu.Unlock()
	if last {
		g.cancel()
	}
	if last || ctx.Err() == nil {
		<-g.cleaned
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return result.supported, result.err
}
func (cc *Conn) freezeQBlockProbeLocked(g *qblockProbeGeneration, supported bool, err error, evidence qblockCapability) {
	if cc.qblockGeneration != g || g.terminal {
		return
	}
	if closed := cc.Context().Err(); closed != nil {
		supported = false
		err = closed
		cc.qblockKnowledge = qblockCapabilityUnknown
		evidence = qblockCapabilityUnknown
	} else if !time.Now().Before(g.deadline) {
		supported = false
		err = context.DeadlineExceeded
		evidence = qblockCapabilityUnknown
	}
	g.terminal = true
	g.result = qblockCapabilityResult{supported, err}
	if err == nil && evidence != qblockCapabilityUnknown {
		cc.qblockKnowledge = evidence
	}
	close(g.done)
}
func (cc *Conn) publishQBlockProbe(g *qblockProbeGeneration, supported bool, err error, evidence qblockCapability) {
	cc.qblockProbeMu.Lock()
	cc.freezeQBlockProbeLocked(g, supported, err, evidence)
	cc.qblockProbeMu.Unlock()
}
func (cc *Conn) runQBlockProbe(g *qblockProbeGeneration) {
	supported, err := cc.probeQBlockWire(g.context, g.path, g)
	cc.publishQBlockProbe(g, supported, err, qblockCapabilityUnknown)
	g.cancel()
	cc.qblockProbeMu.Lock()
	if cc.qblockGeneration == g {
		cc.qblockGeneration = nil
		cc.qblockProbe = nil
	}
	close(g.cleaned)
	cc.qblockProbeMu.Unlock()
}
