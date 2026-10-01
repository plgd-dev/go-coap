package client

import (
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

// The domain never enters a connection or invokes a callback. Members provide
// stable, never-closed wake channels; only nonblocking signals leave its lock.
type qblockEndpointDomain struct {
	mu                     sync.Mutex
	clock                  qblockClock
	rate                   uint64
	maxPeers, maxMembers   uint32
	nextMember, nextTicket uint64
	peers                  map[netip.AddrPort]*qblockEndpointState
	members                map[uint64]*qblockEndpointMember
	closed                 bool
}
type qblockEndpointState struct {
	gate                      *qblockProbeGate
	owner                     uint64
	attempts                  uint32
	settleRequested, answered bool
	completed                 time.Time
	waiters                   map[uint64]qblockEndpointCandidate
}
type qblockEndpointCandidate struct {
	key    qblockProbeKey
	ticket uint64
}
type qblockEndpointMember struct {
	domain   *qblockEndpointDomain
	peer     netip.AddrPort
	id       uint64
	wake     chan struct{}
	detached bool
}

func newQBlockEndpointDomain(clock qblockClock, rate uint64, peers, members uint32) *qblockEndpointDomain {
	return &qblockEndpointDomain{clock: clock, rate: rate, maxPeers: peers, maxMembers: members, peers: make(map[netip.AddrPort]*qblockEndpointState), members: make(map[uint64]*qblockEndpointMember)}
}
func qblockEndpointPeer(addr net.Addr) (netip.AddrPort, error) {
	a, ok := addr.(*net.UDPAddr)
	if !ok || a == nil || a.Port <= 0 || a.Port > 65535 {
		return netip.AddrPort{}, qblock.ErrLimitExceeded
	}
	ip, ok := netip.AddrFromSlice(a.IP)
	if !ok {
		return netip.AddrPort{}, qblock.ErrLimitExceeded
	}
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() {
		return netip.AddrPort{}, qblock.ErrLimitExceeded
	}
	if ip.Is6() && a.Zone != "" {
		ip = ip.WithZone(a.Zone)
	}
	return netip.AddrPortFrom(ip, uint16(a.Port)), nil
}
func (d *qblockEndpointDomain) attach(addr net.Addr, wake chan struct{}, now time.Time) (*qblockEndpointMember, error) {
	peer, err := qblockEndpointPeer(addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneLocked(now)
	if d.closed || d.rate == 0 || uint64(len(d.members)) >= uint64(d.maxMembers) || d.nextMember == math.MaxUint64 {
		return nil, qblock.ErrLimitExceeded
	}
	if d.peers[peer] == nil {
		if uint64(len(d.peers)) >= uint64(d.maxPeers) {
			return nil, qblock.ErrLimitExceeded
		}
		d.peers[peer] = &qblockEndpointState{gate: newQBlockProbeGate(d.rate), waiters: make(map[uint64]qblockEndpointCandidate)}
	}
	d.nextMember++
	m := &qblockEndpointMember{domain: d, peer: peer, id: d.nextMember, wake: wake}
	d.members[m.id] = m
	return m, nil
}
func (d *qblockEndpointDomain) wakeLocked(peer netip.AddrPort) []chan struct{} {
	var wakes []chan struct{}
	for _, m := range d.members {
		if m.peer == peer && m.wake != nil {
			wakes = append(wakes, m.wake)
		}
	}
	return wakes
}
func qblockEndpointNotify(wakes []chan struct{}) {
	for _, w := range wakes {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}
func (d *qblockEndpointDomain) pruneLocked(now time.Time) {
	for peer, s := range d.peers {
		s.gate.ready(now)
		if s.gate.state == qblockProbeOpen {
			s.owner = 0
		}
		hasMember := false
		for _, m := range d.members {
			if m.peer == peer {
				hasMember = true
				break
			}
		}
		if !hasMember && s.attempts == 0 && s.gate.state == qblockProbeOpen {
			delete(d.peers, peer)
		}
	}
}
func (m *qblockEndpointMember) stateLocked() *qblockEndpointState { return m.domain.peers[m.peer] }
func (m *qblockEndpointMember) eligibleLocked(s *qblockEndpointState, now time.Time) bool {
	if m.detached || m.domain.closed || s == nil || s.attempts > 0 || !s.gate.ready(now) {
		return false
	}
	if s.gate.state == qblockProbeOpen {
		s.owner = 0
	}
	var oldest uint64
	for id, c := range s.waiters {
		if oldest == 0 || c.ticket < s.waiters[oldest].ticket {
			oldest = id
		}
	}
	return oldest == 0 || oldest == m.id
}
func (m *qblockEndpointMember) candidateReady(key qblockProbeKey, now time.Time) bool {
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	if m.detached || d.closed || s == nil || key == 0 {
		d.mu.Unlock()
		return false
	}
	c, ok := s.waiters[m.id]
	if !ok {
		if d.nextTicket == math.MaxUint64 {
			d.mu.Unlock()
			return false
		}
		d.nextTicket++
		c.ticket = d.nextTicket
	}
	c.key = key
	s.waiters[m.id] = c
	ready := m.eligibleLocked(s, now)
	d.mu.Unlock()
	return ready
}
func (m *qblockEndpointMember) ready(now time.Time) bool {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	return m.eligibleLocked(m.stateLocked(), now)
}
func (m *qblockEndpointMember) admit(key qblockProbeKey, kind qblockProbeKind, wait time.Duration, now time.Time) bool {
	if !m.candidateReady(key, now) {
		return false
	}
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	ok := m.eligibleLocked(s, now) && s.gate.admit(key, kind, wait, now)
	if ok {
		s.owner = m.id
		s.settleRequested = false
		s.answered = false
		s.completed = time.Time{}
		delete(s.waiters, m.id)
	}
	d.mu.Unlock()
	return ok
}
func (m *qblockEndpointMember) ownsActive(key qblockProbeKey) bool {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := m.stateLocked()
	return !m.detached && s != nil && s.owner == m.id && s.gate.key == key && s.gate.state == qblockProbeActive
}
func (m *qblockEndpointMember) owns(key qblockProbeKey) bool {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := m.stateLocked()
	return !m.detached && s != nil && s.owner == m.id && s.gate.key == key && s.gate.state != qblockProbeOpen
}
func (m *qblockEndpointMember) kindOf(key qblockProbeKey) qblockProbeKind {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := m.stateLocked()
	if s != nil && s.owner == m.id && s.gate.key == key {
		return s.gate.kind
	}
	return 0
}
func (m *qblockEndpointMember) beginAttempt(key qblockProbeKey, bytes uint64) bool {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := m.stateLocked()
	if m.detached || d.closed || s == nil || s.owner != m.id || s.gate.key != key || s.gate.state != qblockProbeActive || s.attempts == math.MaxUint32 {
		return false
	}
	s.gate.charge(key, bytes)
	s.attempts++
	return true
}
func (s *qblockEndpointState) finishLocked(now time.Time) {
	if s.attempts != 0 {
		return
	}
	if s.answered {
		s.gate.reset()
		s.owner = 0
		s.answered = false
	} else if s.settleRequested {
		if now.Before(s.completed) {
			now = s.completed
		}
		s.gate.settle(s.gate.key, now)
		if s.gate.state == qblockProbeOpen {
			s.owner = 0
		}
	}
	s.settleRequested = false
}
func (m *qblockEndpointMember) endAttempt(key qblockProbeKey, now time.Time) {
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	if s == nil || s.owner != m.id || s.gate.key != key || s.attempts == 0 {
		d.mu.Unlock()
		return
	}
	s.attempts--
	if now.After(s.completed) {
		s.completed = now
	}
	s.finishLocked(now)
	w := d.wakeLocked(m.peer)
	d.mu.Unlock()
	qblockEndpointNotify(w)
}
func (m *qblockEndpointMember) settle(key qblockProbeKey, now time.Time) {
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	if s == nil {
		d.mu.Unlock()
		return
	}
	if c, ok := s.waiters[m.id]; ok && c.key == key {
		delete(s.waiters, m.id)
	}
	if s.owner == m.id && s.gate.key == key {
		s.settleRequested = true
		s.finishLocked(now)
	}
	w := d.wakeLocked(m.peer)
	d.mu.Unlock()
	qblockEndpointNotify(w)
}
func (m *qblockEndpointMember) feedback(key qblockProbeKey) bool {
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	if m.detached || d.closed || s == nil || s.owner != m.id || s.gate.key != key || s.gate.bytes == 0 {
		d.mu.Unlock()
		return false
	}
	s.answered = true
	s.finishLocked(d.clock.Now())
	w := d.wakeLocked(m.peer)
	d.mu.Unlock()
	qblockEndpointNotify(w)
	return true
}
func (m *qblockEndpointMember) nextDeadline() (time.Time, bool) {
	d := m.domain
	d.mu.Lock()
	defer d.mu.Unlock()
	s := m.stateLocked()
	if m.detached || d.closed || s == nil || s.attempts > 0 {
		return time.Time{}, false
	}
	return s.gate.nextDeadline()
}
func (m *qblockEndpointMember) detach(now time.Time) {
	d := m.domain
	d.mu.Lock()
	if m.detached {
		d.mu.Unlock()
		return
	}
	m.detached = true
	delete(d.members, m.id)
	s := m.stateLocked()
	if s != nil {
		delete(s.waiters, m.id)
		if s.owner == m.id {
			s.settleRequested = true
			s.finishLocked(now)
		}
	}
	w := d.wakeLocked(m.peer)
	d.pruneLocked(now)
	d.mu.Unlock()
	qblockEndpointNotify(w)
}
func (d *qblockEndpointDomain) close() {
	d.mu.Lock()
	d.closed = true
	var wakes []chan struct{}
	for _, m := range d.members {
		if m.wake != nil {
			wakes = append(wakes, m.wake)
		}
	}
	d.mu.Unlock()
	qblockEndpointNotify(wakes)
}

func (m *qblockEndpointMember) withdraw(key qblockProbeKey) {
	d := m.domain
	d.mu.Lock()
	s := m.stateLocked()
	if s != nil {
		if candidate, ok := s.waiters[m.id]; ok && candidate.key == key {
			delete(s.waiters, m.id)
		}
	}
	w := d.wakeLocked(m.peer)
	d.mu.Unlock()
	qblockEndpointNotify(w)
}
