// Package qblocklink provides a caller-serialized, socket-free fault relay.
package qblocklink

import (
	"bytes"
	"errors"
	"fmt"
)

type Direction uint8

const (
	ClientToServer Direction = iota
	ServerToClient
)

type Kind string

const (
	Ordinary  Kind = "ordinary"
	Malformed Kind = "malformed"
	Q1        Kind = "q1"
	Q2        Kind = "q2"
	Continue  Kind = "continue"
	Missing   Kind = "missing"
	ACK       Kind = "ack"
	Reset     Kind = "reset"
)

type Action string

const (
	Pass      Action = "pass"
	Drop      Action = "drop"
	Hold      Action = "hold"
	Duplicate Action = "duplicate"
	Released  Action = "release"
)

type Rule struct {
	Direction  Direction
	Kind       Kind
	Occurrence uint64
	Action     Action
}
type Limits struct {
	MaxEvents int
	MaxBytes  int
}
type Packet struct {
	ID        uint64
	Direction Direction
	Wire      []byte
}
type Event struct {
	Packet
	Kind       Kind
	Occurrence uint64
	Action     Action
}

// Link is not concurrent-safe: callers serialize packet and release events.
// Retention limits count chronological trace events and their complete wires.
type Link struct {
	rules    map[selector]Action
	counts   map[stream]uint64
	held     map[uint64]int
	events   []Event
	limits   Limits
	retained int
	next     uint64
}
type stream struct {
	direction Direction
	kind      Kind
}
type selector struct {
	stream
	occurrence uint64
}

func validKind(k Kind) bool {
	switch k {
	case Ordinary, Malformed, Q1, Q2, Continue, Missing, ACK, Reset:
		return true
	}
	return false
}

// New copies and validates the immutable script. Each selector is unique.
func New(rules []Rule, limits Limits) (*Link, error) {
	if limits.MaxEvents <= 0 || limits.MaxBytes <= 0 {
		return nil, errors.New("positive retention limits required")
	}
	l := &Link{rules: make(map[selector]Action), counts: make(map[stream]uint64), held: make(map[uint64]int), limits: limits}
	for _, r := range rules {
		if r.Direction > ServerToClient || !validKind(r.Kind) || r.Occurrence == 0 {
			return nil, errors.New("invalid rule selector")
		}
		switch r.Action {
		case Pass, Drop, Hold, Duplicate:
		default:
			return nil, errors.New("invalid rule action")
		}
		key := selector{stream{r.Direction, r.Kind}, r.Occurrence}
		if _, ok := l.rules[key]; ok {
			return nil, errors.New("duplicate selector")
		}
		l.rules[key] = r.Action
	}
	return l, nil
}
func clonePacket(p Packet) Packet { p.Wire = bytes.Clone(p.Wire); return p }
func (l *Link) room(events, size int) bool {
	return events <= l.limits.MaxEvents-len(l.events) && size <= l.limits.MaxBytes-l.retained
}

// Process consumes one input occurrence. Rejections change no relay state.
func (l *Link) Process(d Direction, w []byte) ([]Packet, error) {
	if d > ServerToClient {
		return nil, errors.New("invalid direction")
	}
	if !l.room(1, len(w)) {
		return nil, errors.New("trace retention exhausted")
	}
	k := classify(w)
	key := stream{d, k}
	occ := l.counts[key] + 1
	action := l.rules[selector{key, occ}]
	if action == "" {
		action = Pass
	}
	l.next++
	p := Packet{l.next, d, bytes.Clone(w)}
	e := Event{p, k, occ, action}
	l.events = append(l.events, e)
	l.retained += len(w)
	l.counts[key] = occ
	switch action {
	case Drop:
		return nil, nil
	case Hold:
		l.held[p.ID] = len(l.events) - 1
		return nil, nil
	case Duplicate:
		return []Packet{clonePacket(p), clonePacket(p)}, nil
	default:
		return []Packet{clonePacket(p)}, nil
	}
}

// Release emits held inputs in the supplied order, without reapplying rules.
// Unknown/duplicate IDs or retention exhaustion reject the entire operation.
func (l *Link) Release(ids ...uint64) ([]Packet, error) {
	seen := make(map[uint64]bool)
	size := 0
	for _, id := range ids {
		i, ok := l.held[id]
		if !ok || seen[id] {
			return nil, fmt.Errorf("not a unique held input: %d", id)
		}
		seen[id] = true
		n := len(l.events[i].Wire)
		if n > l.limits.MaxBytes-size {
			return nil, errors.New("trace retention exhausted")
		}
		size += n
	}
	if !l.room(len(ids), size) {
		return nil, errors.New("trace retention exhausted")
	}
	var out []Packet
	for _, id := range ids {
		e := l.events[l.held[id]]
		delete(l.held, id)
		e.Packet = clonePacket(e.Packet)
		e.Action = Released
		l.events = append(l.events, e)
		l.retained += len(e.Wire)
		out = append(out, clonePacket(e.Packet))
	}
	return out, nil
}

// Trace returns detached chronological input/release evidence, including drops.
func (l *Link) Trace() []Event {
	out := make([]Event, len(l.events))
	for i, e := range l.events {
		e.Packet = clonePacket(e.Packet)
		out[i] = e
	}
	return out
}

// classify intentionally does not use the production CoAP decoder. Raw option
// extensions and Q values are checked without allocating decoded options.
func classify(w []byte) Kind {
	if len(w) < 4 || w[0]>>6 != 1 || w[0]&15 > 8 || len(w) < 4+int(w[0]&15) {
		return Malformed
	}
	typ := (w[0] >> 4) & 3
	code := w[1]
	if code == 0 {
		if len(w) != 4 || w[0]&15 != 0 {
			return Malformed
		}
		if typ == 2 {
			return ACK
		}
		if typ == 3 {
			return Reset
		}
		return Ordinary
	}
	if typ == 3 {
		return Malformed
	}
	off := 4 + int(w[0]&15)
	number := 0
	hasQ1, hasQ2, missing := false, false, false
	q1count := 0
	q2count := 0
	for off < len(w) {
		if w[off] == 255 {
			if off+1 == len(w) {
				return Malformed
			}
			break
		}
		h := w[off]
		off++
		delta, ok := extended(w, &off, int(h>>4))
		if !ok {
			return Malformed
		}
		length, ok := extended(w, &off, int(h&15))
		if !ok || length > len(w)-off {
			return Malformed
		}
		number += delta
		if number > 65535 {
			return Malformed
		}
		value := w[off : off+length]
		off += length
		switch number {
		case 19, 31:
			if length > 3 || (length > 0 && value[length-1]&7 == 7) {
				return Malformed
			}
			if number == 19 {
				hasQ1 = true
				q1count++
			} else {
				hasQ2 = true
				q2count++
			}
		case 12:
			if length <= 2 {
				v := 0
				for _, b := range value {
					v = v*256 + int(b)
				}
				missing = v == 272
			}
		}
	}
	if q1count > 1 || (code >= 64 && q2count > 1) {
		return Malformed
	}
	if code == 95 {
		return Continue
	}
	if code == 136 && missing {
		return Missing
	}
	if hasQ1 {
		return Q1
	}
	if hasQ2 {
		return Q2
	}
	return Ordinary
}
func extended(w []byte, off *int, n int) (int, bool) {
	switch n {
	case 15:
		return 0, false
	case 13:
		if *off >= len(w) {
			return 0, false
		}
		v := 13 + int(w[*off])
		*off++
		return v, true
	case 14:
		if len(w)-*off < 2 {
			return 0, false
		}
		v := 269 + int(w[*off])*256 + int(w[*off+1])
		*off += 2
		return v, true
	default:
		return n, true
	}
}
