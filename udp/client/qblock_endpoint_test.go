package client

import (
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
	"net"
	"testing"
	"time"
)

func endpointPeer(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}
func TestQBlockEndpointSharedFIFOAndReconnect(t *testing.T) {
	now := time.Unix(100, 0)
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 4, 4)
	a, err := d.attach(endpointPeer(123), make(chan struct{}, 1), now)
	require.NoError(t, err)
	b, err := d.attach(endpointPeer(123), make(chan struct{}, 1), now)
	require.NoError(t, err)
	require.True(t, a.admit(1, qblockProbeControl, 0, now))
	require.True(t, a.beginAttempt(1, 2))
	require.False(t, b.candidateReady(1, now))
	a.endAttempt(1, now)
	a.settle(1, now)
	a.detach(now)
	c, err := d.attach(endpointPeer(123), make(chan struct{}, 1), now)
	require.NoError(t, err)
	require.False(t, c.candidateReady(1, now))
	require.False(t, c.admit(1, qblockProbeControl, 0, now))
	require.True(t, b.candidateReady(1, now.Add(2*time.Second)))
	require.True(t, b.admit(1, qblockProbeControl, 0, now.Add(2*time.Second)))
	require.False(t, a.feedback(1))
	require.True(t, b.ownsActive(1))
	require.True(t, b.beginAttempt(1, 1))
	b.endAttempt(1, now.Add(2*time.Second))
	require.True(t, b.feedback(1))
	require.True(t, c.candidateReady(1, now.Add(2*time.Second)))
}
func TestQBlockEndpointActiveWriteCloseAndFeedback(t *testing.T) {
	now := time.Unix(100, 0)
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 2, 2)
	a, _ := d.attach(endpointPeer(123), make(chan struct{}, 1), now)
	b, _ := d.attach(endpointPeer(123), make(chan struct{}, 1), now)
	require.True(t, a.admit(9, qblockProbeBody, time.Second, now))
	require.True(t, a.beginAttempt(9, 50))
	a.detach(now)
	require.False(t, b.admit(9, qblockProbeControl, 0, now.Add(10*time.Second)))
	a.endAttempt(9, now.Add(10*time.Second))
	require.False(t, b.candidateReady(9, now.Add(10*time.Second)))
	require.True(t, b.admit(9, qblockProbeControl, 0, now.Add(11*time.Second)))
	require.True(t, b.beginAttempt(9, 5))
	require.True(t, b.feedback(9))
	require.False(t, b.ready(now.Add(11*time.Second)))
	b.endAttempt(9, now.Add(11*time.Second))
	require.True(t, b.ready(now.Add(11*time.Second)))
}
func TestQBlockEndpointNormalizationAndBounds(t *testing.T) {
	now := time.Unix(100, 0)
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 2, 3)
	a, err := d.attach(&net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: 123}, nil, now)
	require.NoError(t, err)
	b, err := d.attach(endpointPeer(123), nil, now)
	require.NoError(t, err)
	require.Equal(t, a.peer, b.peer)
	c, err := d.attach(endpointPeer(124), nil, now)
	require.NoError(t, err)
	_, err = d.attach(endpointPeer(125), nil, now)
	require.Error(t, err)
	require.True(t, a.admit(1, qblockProbeControl, 0, now))
	require.True(t, c.admit(1, qblockProbeControl, 0, now))
	a.settle(1, now)
	require.True(t, b.admit(1, qblockProbeControl, 0, now), "zero-attempt cancellation releases")
	_, err = d.attach(&net.UDPAddr{IP: net.ParseIP("ff02::1"), Port: 123}, nil, now)
	require.Error(t, err)
	x, _ := qblockEndpointPeer(&net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 123, Zone: "en0"})
	y, _ := qblockEndpointPeer(&net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 123, Zone: "en1"})
	require.NotEqual(t, x, y)
}

func TestQBlockEndpointAdapterCoordinatesConnections(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	d := newQBlockEndpointDomain(clock, 1, 2, 2)
	makeConn := func() *Conn {
		session := &qblockEndpointTestSession{qblockTestSession: qblockTestSession{ctx: context.Background()}}
		cfg := DefaultConfig
		cfg.BlockwiseEnable = false
		cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: clock, Endpoint: d}))
		t.Cleanup(session.closeForTest)
		return cc
	}
	a, b := makeConn(), makeConn()
	for i, cc := range []*Conn{a, b} {
		req := newPrivateQBlockClientGET(t, cc, message.Token{byte(i + 1)})
		defer cc.ReleaseMessage(req)
		_, err := cc.qblockClient.prepare(req, nil)
		require.NoError(t, err)
		cc.qblockClient.drivePending(now)
	}
	require.Len(t, a.session.(*qblockEndpointTestSession).writesSnapshot(), 1)
	require.Empty(t, b.session.(*qblockEndpointTestSession).writesSnapshot(), "same peer shares unanswered debt")
	a.qblockClient.close()
	b.qblockClient.drivePending(now)
	require.Empty(t, b.session.(*qblockEndpointTestSession).writesSnapshot(), "detach retains debt")
}

type qblockEndpointTestSession struct{ qblockTestSession }

func (s *qblockEndpointTestSession) RemoteAddr() net.Addr { return endpointPeer(123) }

func TestQBlockEndpointSchedulerWakesAfterFeedback(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	d := newQBlockEndpointDomain(clock, 1, 2, 2)
	makeConn := func(token byte) *Conn {
		session := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
		cfg := DefaultConfig
		cfg.BlockwiseEnable = false
		cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleAutomatic, Endpoint: d}))
		t.Cleanup(session.closeForTest)
		req := newPrivateQBlockClientGET(t, cc, message.Token{token})
		defer cc.ReleaseMessage(req)
		_, err := cc.qblockClient.prepare(req, nil)
		require.NoError(t, err)
		return cc
	}
	a := makeConn(1)
	require.Eventually(t, func() bool { return len(a.session.(*qblockTestSession).writesSnapshot()) == 1 }, time.Second, time.Millisecond)
	b := makeConn(2)
	require.Eventually(t, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.peers[a.qblockClient.endpoint.peer].waiters) == 1
	}, time.Second, time.Millisecond)
	require.Empty(t, b.session.(*qblockTestSession).writesSnapshot())
	a.qblockClient.mu.Lock()
	key := a.qblockClient.currentProbe.key
	require.True(t, a.qblockClient.acceptPacingFeedbackLocked(key))
	a.qblockClient.mu.Unlock()
	require.Eventually(t, func() bool { return len(b.session.(*qblockTestSession).writesSnapshot()) == 1 }, time.Second, time.Millisecond)
}

func TestQBlockEndpointClearingPendingWithdrawsCandidate(t *testing.T) {
	now := time.Unix(100, 0)
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 2, 3)
	a, _ := d.attach(endpointPeer(123), nil, now)
	b, _ := d.attach(endpointPeer(123), nil, now)
	c, _ := d.attach(endpointPeer(123), nil, now)
	require.True(t, a.admit(1, qblockProbeControl, 0, now))
	require.True(t, a.beginAttempt(1, 1))
	a.endAttempt(1, now)
	a.settle(1, now)
	client := &qblockClient{endpoint: b, workQueue: newQBlockWorkQueue(2, 1024)}
	id, err := client.workQueue.reserve(64)
	require.NoError(t, err)
	client.workQueue.onClear = client.withdrawPending
	require.NoError(t, client.workQueue.replace(id, qblockPendingWork{Kind: qblockWorkGET, ProbeKey: 2}, false))
	_, _ = client.workQueue.nextDeadline(now, b)
	require.False(t, c.candidateReady(3, now))
	client.workQueue.clearPending(id)
	require.True(t, c.candidateReady(3, now.Add(time.Second)))
}

func TestQBlockEndpointInvalidInitializationRollsBackMember(t *testing.T) {
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 2, 2)
	session := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Endpoint: d, MaxOwnedBytes: 1}))
	require.Error(t, cc.qblockClient.initErr)
	d.mu.Lock()
	require.Empty(t, d.members)
	d.mu.Unlock()
	session.closeForTest()
}
func TestQBlockEndpointClosedDomainHidesDebtDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	d := newQBlockEndpointDomain(realQBlockClock{}, 1, 2, 2)
	m, _ := d.attach(endpointPeer(123), nil, now)
	require.True(t, m.admit(1, qblockProbeControl, 0, now))
	require.True(t, m.beginAttempt(1, 1))
	m.endAttempt(1, now)
	m.settle(1, now)
	d.close()
	_, ok := m.nextDeadline()
	require.False(t, ok, "closed gate must not publish ineligible debt deadline")
}
