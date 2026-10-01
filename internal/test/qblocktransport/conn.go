// Package qblocktransport adapts qblocklink to test datagram transports.
package qblocktransport

import (
	"context"
	"errors"
	"io"
	"net"
	"sort"
	"sync"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
)

type Forward struct {
	Phase     string
	Direction qblocklink.Direction
	ID        uint64
	Count     int
}
type Snapshot struct {
	Probe, Faults []qblocklink.Event
	Forwarded     []Forward
}
type packet struct {
	qblocklink.Packet
	phase string
}
type forwardKey struct {
	phase     string
	direction qblocklink.Direction
	id        uint64
}
type Controller struct {
	mu            sync.Mutex
	probe, faults *qblocklink.Link
	forwarded     map[forwardKey]int
}

func NewController() *Controller {
	probe, _ := qblocklink.New(nil, traceLimits())
	return &Controller{probe: probe, forwarded: make(map[forwardKey]int)}
}
func traceLimits() qblocklink.Limits { return qblocklink.Limits{MaxEvents: 4096, MaxBytes: 4 << 20} }

// Arm starts fault occurrence counts after the public capability probe.
func (c *Controller) Arm(rules []qblocklink.Rule) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.faults != nil {
		return errors.New("faults already armed")
	}
	l, err := qblocklink.New(rules, traceLimits())
	if err != nil {
		return err
	}
	c.faults = l
	return nil
}
func (c *Controller) process(d qblocklink.Direction, wire []byte) ([]packet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.probe
	phase := "probe"
	if c.faults != nil {
		l = c.faults
		phase = "faults"
	}
	packets, err := l.Process(d, wire)
	if err != nil {
		return nil, err
	}
	out := make([]packet, len(packets))
	for i, p := range packets {
		out[i] = packet{Packet: p, phase: phase}
	}
	return out, nil
}
func (c *Controller) delivered(p packet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forwarded[forwardKey{p.phase, p.Direction, p.ID}]++
}
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Snapshot{Probe: c.probe.Trace()}
	if c.faults != nil {
		s.Faults = c.faults.Trace()
	}
	for key, count := range c.forwarded {
		s.Forwarded = append(s.Forwarded, Forward{key.phase, key.direction, key.id, count})
	}
	sort.Slice(s.Forwarded, func(i, j int) bool {
		a, b := s.Forwarded[i], s.Forwarded[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Direction < b.Direction
	})
	return s
}

// PlaintextConn wraps an accepted authenticated datagram connection. Read sees
// plaintext after DTLS authentication/decryption; Write sees plaintext before
// encryption. No fault selector examines encrypted UDP records.
type PlaintextConn struct {
	net.Conn
	controller      *Controller
	readMu, writeMu sync.Mutex
	pending         []packet
}

func NewPlaintextConn(c net.Conn, controller *Controller) *PlaintextConn {
	return &PlaintextConn{Conn: c, controller: controller}
}
func (c *PlaintextConn) HandshakeContext(ctx context.Context) error {
	if h, ok := c.Conn.(interface{ HandshakeContext(context.Context) error }); ok {
		return h.HandshakeContext(ctx)
	}
	return ctx.Err()
}
func (c *PlaintextConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if len(c.pending) > 0 {
			p := c.pending[0]
			c.pending = c.pending[1:]
			if len(p.Wire) > len(b) {
				return 0, io.ErrShortBuffer
			}
			n := copy(b, p.Wire)
			c.controller.delivered(p)
			return n, nil
		}
		n, err := c.Conn.Read(b)
		if err != nil {
			return n, err
		}
		packets, err := c.controller.process(qblocklink.ClientToServer, b[:n])
		if err != nil {
			return 0, err
		}
		c.pending = append(c.pending, packets...)
	}
}
func (c *PlaintextConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	packets, err := c.controller.process(qblocklink.ServerToClient, b)
	if err != nil {
		return 0, err
	}
	for _, packet := range packets {
		n, err := c.Conn.Write(packet.Wire)
		if err != nil {
			return 0, err
		}
		if n != len(packet.Wire) {
			return 0, io.ErrShortWrite
		}
		c.controller.delivered(packet)
	}
	return len(b), nil
}
