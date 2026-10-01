package client

import (
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func ordinaryEndpointConn(t *testing.T) (*Conn, *fakeQBlockClock) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	d := newQBlockEndpointDomain(clock, 1, 4, 8)
	session := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: clock, Endpoint: d}))
	t.Cleanup(session.closeForTest)
	return cc, clock
}
func TestQBlockOrdinaryAdmissionCancellationBeforeMIDClone(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	m := cc.qblockClient.endpoint
	require.True(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
	require.True(t, m.beginAttempt(1, 2))
	m.endAttempt(1, clock.Now())
	m.settle(1, clock.Now())
	ctx, cancel := context.WithCancel(context.Background())
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	req.SetToken(message.Token{1})
	req.SetMessageID(17)
	done := make(chan error, 1)
	go func() { done <- cc.writeMessage(req) }()
	require.Eventually(t, func() bool {
		m.domain.mu.Lock()
		defer m.domain.mu.Unlock()
		return len(m.domain.peers[m.peer].waiters) == 1
	}, time.Second, time.Millisecond)
	require.Equal(t, 0, cc.midHandlerContainer.Length())
	require.Empty(t, cc.session.(*qblockTestSession).writesSnapshot())
	cancel()
	require.Error(t, <-done)
	m.domain.mu.Lock()
	require.Len(t, m.domain.members, 1)
	m.domain.mu.Unlock()
}
func TestQBlockOrdinaryNONFeedbackReleasesQ(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetToken(message.Token{8})
	req.SetMessageID(18)
	require.NoError(t, cc.writeMessage(req))
	m := cc.qblockClient.endpoint
	require.False(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
	resp := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(resp)
	resp.SetCode(codes.Content)
	resp.SetType(message.NonConfirmable)
	resp.SetToken(message.Token{9})
	require.False(t, cc.acceptOrdinaryResponse(resp))
	require.False(t, m.ready(clock.Now()))
	resp.SetToken(message.Token{8})
	require.True(t, cc.acceptOrdinaryResponse(resp))
	require.True(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
}

func TestQBlockOrdinaryRetransmitKeepsOwnerAndRejectsRequestMID(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	req.SetToken(message.Token{10})
	req.SetMessageID(22)
	p, err := cc.acquireOrdinary(req)
	require.NoError(t, err)
	closeFn, err := cc.prepareWriteMessage(req, func(_ *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {}, p)
	require.NoError(t, err)
	defer closeFn()
	require.NoError(t, cc.writeOrdinary(req, p))
	elem, ok := cc.midHandlerContainer.Load(22)
	require.True(t, ok)
	elem.start = clock.Now()
	cc.checkMidHandlerContainer(clock.Now().Add(2*time.Second), 4, time.Second, 22, elem)
	require.Len(t, cc.session.(*qblockTestSession).writesSnapshot(), 2)
	require.True(t, p.member.ownsActive(1))
	unrelated := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(unrelated)
	unrelated.SetCode(codes.POST)
	unrelated.SetType(message.NonConfirmable)
	unrelated.SetMessageID(22)
	require.False(t, cc.handleSpecialMessages(unrelated))
	_, ok = cc.midHandlerContainer.Load(22)
	require.True(t, ok)
	ack := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(ack)
	ack.SetCode(codes.Empty)
	ack.SetType(message.Acknowledgement)
	ack.SetMessageID(22)
	cc.handleSpecialMessages(ack)
	require.False(t, p.member.owns(1))
	require.True(t, cc.qblockClient.endpoint.ready(clock.Now()))
}

func TestQBlockOrdinaryClosedDomainWaitReturns(t *testing.T) {
	cc, _ := ordinaryEndpointConn(t)
	cc.ordinaryDomain().close()
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	done := make(chan error, 1)
	go func() { done <- cc.writeMessage(req) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("closed domain must reject, not wait")
	}
}

func TestQBlockOrdinaryInvalidNONReleasesOwner(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(-1)
	p, err := cc.acquireOrdinary(req)
	require.NoError(t, err)
	require.Error(t, cc.writeOrdinary(req, p))
	require.True(t, cc.qblockClient.endpoint.ready(clock.Now()))
}
func TestQBlockOrdinarySeparateResponseCompletesMID(t *testing.T) {
	cc, _ := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	p, err := cc.acquireOrdinary(req)
	require.NoError(t, err)
	calls := 0
	closeFn, err := cc.prepareWriteMessage(req, func(_ *responsewriter.ResponseWriter[*Conn], _ *pool.Message) { calls++ }, p)
	require.NoError(t, err)
	defer closeFn()
	require.NoError(t, cc.writeOrdinary(req, p))
	resp := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(resp)
	resp.SetCode(codes.Content)
	resp.SetType(message.NonConfirmable)
	resp.SetToken([]byte{1})
	require.True(t, cc.acceptOrdinaryResponse(resp))
	require.Equal(t, 1, calls)
	_, ok := cc.midHandlerContainer.Load(1)
	require.False(t, ok)
}
func TestQBlockOrdinaryFeedbackRequiresAttemptAndValidForm(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.Confirmable)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	p, err := cc.acquireOrdinary(req)
	require.NoError(t, err)
	defer p.finish(false, clock.Now())
	resp := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(resp)
	resp.SetCode(codes.Content)
	resp.SetType(message.Acknowledgement)
	resp.SetToken([]byte{1})
	resp.SetMessageID(1)
	require.False(t, cc.acceptOrdinaryResponse(resp), "before attempted write")
	require.True(t, p.member.ownsActive(1))
	require.NoError(t, cc.writeOrdinary(req, p))
	resp.SetMessageID(2)
	require.False(t, cc.acceptOrdinaryResponse(resp), "wrong ACK MID")
	resp.SetType(message.Reset)
	resp.SetMessageID(1)
	require.False(t, cc.acceptOrdinaryResponse(resp), "Reset is not a representation")
	require.True(t, p.member.owns(1))
}
func TestQBlockOrdinaryExpirationReclaimsMember(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	require.NoError(t, cc.writeMessage(req))
	clock.Advance(time.Hour)
	cc.CheckExpirations(clock.Now())
	cc.ordinaryMu.Lock()
	count := len(cc.ordinary)
	cc.ordinaryMu.Unlock()
	require.Zero(t, count)
}
func TestQBlockOrdinaryClosureAfterAttachmentReturns(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	m := cc.qblockClient.endpoint
	require.True(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(2)
	done := make(chan error, 1)
	go func() { done <- cc.writeMessage(req) }()
	require.Eventually(t, func() bool { m.domain.mu.Lock(); defer m.domain.mu.Unlock(); return len(m.domain.members) == 2 }, time.Second, time.Millisecond)
	m.domain.close()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("closed domain waiter blocked")
	}
}

func TestQBlockOrdinaryImmediateReplyBypassesDebt(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	m := cc.qblockClient.endpoint
	require.True(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := cc.AcquireMessage(ctx)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	done := make(chan struct{})
	go func() {
		cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("immediate reply waited behind Q debt")
	}
	require.Len(t, cc.session.(*qblockTestSession).writesSnapshot(), 1)
}
func TestQBlockOrdinaryDebtExpiresOnConfiguredClock(t *testing.T) {
	cc, clock := ordinaryEndpointConn(t)
	m := cc.qblockClient.endpoint
	require.True(t, m.admit(1, qblockProbeControl, 0, clock.Now()))
	require.True(t, m.beginAttempt(1, 1))
	m.endAttempt(1, clock.Now())
	m.settle(1, clock.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(2)
	done := make(chan error, 1)
	go func() { done <- cc.writeMessage(req) }()
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	clock.Advance(time.Second)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("injected clock did not release ordinary debt")
	}
}
