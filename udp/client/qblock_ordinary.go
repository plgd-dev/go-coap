package client

import (
	"bytes"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

type qblockOrdinaryPermit struct {
	cc             *Conn
	member         *qblockEndpointMember
	token          message.Token
	mid            int32
	con            bool
	serverResponse bool
	once           sync.Once
}

func (cc *Conn) ordinaryDomain() *qblockEndpointDomain {
	if cc.qblockClient != nil && cc.qblockClient.endpoint != nil {
		return cc.qblockClient.endpoint.domain
	}
	return nil
}
func (cc *Conn) pruneOrdinary(now time.Time) {
	if d := cc.ordinaryDomain(); d != nil {
		d.mu.Lock()
		d.pruneLocked(now)
		d.mu.Unlock()
	}
	cc.ordinaryMu.Lock()
	var stale []*qblockOrdinaryPermit
	for p := range cc.ordinary {
		if !p.con && !p.member.owns(1) {
			stale = append(stale, p)
		}
	}
	cc.ordinaryMu.Unlock()
	for _, p := range stale {
		p.finish(false, now)
	}
}
func (cc *Conn) acquireOrdinary(req *pool.Message) (*qblockOrdinaryPermit, error) {
	d := cc.ordinaryDomain()
	if d == nil || req.Type() == message.Acknowledgement || req.Type() == message.Reset {
		return nil, nil
	}
	now := d.clock.Now()
	d.mu.Lock()
	d.pruneLocked(now)
	d.mu.Unlock()
	cc.pruneOrdinary(now)
	wake := make(chan struct{}, 1)
	m, err := d.attach(cc.RemoteAddr(), wake, now)
	if err != nil {
		return nil, err
	}
	admitted := false
	defer func() {
		if !admitted {
			m.detach(d.clock.Now())
		}
	}()
	cc.receivedMessageReader.TryToReplaceLoop()
	for !m.admit(1, qblockProbeControl, 0, d.clock.Now()) {
		d.mu.Lock()
		closed := d.closed
		d.mu.Unlock()
		if closed {
			return nil, qblock.ErrClosed
		}
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		if cc.Context().Err() != nil {
			return nil, cc.Context().Err()
		}
		// No timer per peer; an ordinary blocked caller owns this bounded timer.
		delay := time.Hour
		if deadline, ok := m.nextDeadline(); ok {
			delay = max(time.Duration(0), deadline.Sub(d.clock.Now()))
		}
		timer := d.clock.NewTimer()
		timer.Reset(delay)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-cc.Context().Done():
			timer.Stop()
			return nil, cc.Context().Err()
		case <-wake:
			timer.Stop()
		case <-timer.C():
		}
	}
	admitted = true
	p := &qblockOrdinaryPermit{cc: cc, member: m, token: cloneQBlockBytes(req.Token()), mid: req.MessageID(), con: req.Type() == message.Confirmable}
	cc.ordinaryMu.Lock()
	if cc.ordinary == nil {
		cc.ordinary = make(map[*qblockOrdinaryPermit]struct{})
	}
	cc.ordinary[p] = struct{}{}
	cc.ordinaryMu.Unlock()
	return p, nil
}
func (p *qblockOrdinaryPermit) write(req *pool.Message) error {
	size, err := qblockDatagramSize(req)
	if err != nil {
		p.finish(false, p.member.domain.clock.Now())
		return err
	}
	if !p.member.beginAttempt(1, size) {
		p.finish(false, p.member.domain.clock.Now())
		return qblock.ErrCanceled
	}
	err = p.cc.session.WriteMessage(req)
	p.member.endAttempt(1, p.member.domain.clock.Now())
	if err != nil {
		p.finish(false, p.member.domain.clock.Now())
	} else if !p.con {
		p.member.settle(1, p.member.domain.clock.Now())
	}
	return err
}
func (p *qblockOrdinaryPermit) finish(answered bool, now time.Time) {
	if p == nil {
		return
	}
	if answered && !p.member.feedback(1) {
		return
	}
	p.once.Do(func() {
		p.member.settle(1, now)
		p.member.detach(now)
		p.cc.ordinaryMu.Lock()
		delete(p.cc.ordinary, p)
		p.cc.ordinaryMu.Unlock()
	})
}
func (cc *Conn) writeOrdinary(req *pool.Message, p *qblockOrdinaryPermit) error {
	if p == nil {
		return cc.session.WriteMessage(req)
	}
	return p.write(req)
}
func (cc *Conn) acceptOrdinaryResponse(msg *pool.Message) bool {
	if (msg.Type() != message.NonConfirmable && msg.Type() != message.Confirmable && msg.Type() != message.Acknowledgement) || msg.Code() < 64 || msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2) || len(msg.Token()) == 0 {
		return false
	}
	cc.ordinaryMu.Lock()
	var matches []*qblockOrdinaryPermit
	for p := range cc.ordinary {
		if !p.serverResponse && bytes.Equal(p.token, msg.Token()) {
			matches = append(matches, p)
		}
	}
	cc.ordinaryMu.Unlock()
	accepted := false
	for _, p := range matches {
		if msg.Type() == message.Acknowledgement && (!p.con || msg.MessageID() != p.mid) {
			continue
		}
		if p.member.feedback(1) {
			p.finish(false, p.member.domain.clock.Now())
			if p.con {
				if elem, ok := cc.midHandlerContainer.LoadAndDelete(p.mid); ok {
					elem.ReleaseMessage(cc)
					resp := cc.AcquireMessage(cc.Context())
					w := responsewriter.New(resp, cc)
					elem.handler(w, msg)
					cc.ReleaseMessage(w.Message())
				}
			}
			accepted = true
		}
	}
	return accepted
}
func (cc *Conn) closeOrdinary() {
	cc.ordinaryMu.Lock()
	var all []*qblockOrdinaryPermit
	for p := range cc.ordinary {
		all = append(all, p)
	}
	cc.ordinaryMu.Unlock()
	for _, p := range all {
		p.finish(false, p.member.domain.clock.Now())
	}
}

func (cc *Conn) validOrdinaryMIDFeedback(p *qblockOrdinaryPermit, msg *pool.Message) bool {
	if msg.MessageID() != p.mid {
		return false
	}
	if msg.Type() == message.Reset || (msg.Type() == message.Acknowledgement && msg.Code() == 0) {
		if msg.Code() != 0 || len(msg.Token()) != 0 || len(msg.Options()) != 0 || msg.Body() != nil {
			return false
		}
	} else if msg.Type() != message.Acknowledgement || msg.Code() < 64 || !bytes.Equal(msg.Token(), p.token) {
		return false
	}
	d := p.member.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := p.member.stateLocked()
	return s != nil && s.owner == p.member.id && s.gate.bytes > 0
}
