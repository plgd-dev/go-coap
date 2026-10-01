package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

func capabilityEndpointConn(t *testing.T) (*Conn, *qblockTestSession, *fakeQBlockClock, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clock := newFakeQBlockClock(time.Unix(100, 0))
	d := newQBlockEndpointDomain(clock, 1, 4, 8)
	s := &qblockTestSession{ctx: ctx, remoteAddr: endpointPeer(123), writeCh: make(chan struct{}, 16)}
	cfg := DefaultConfig
	cc := NewConnWithOpts(&capabilitySession{s}, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: clock, Endpoint: d}))
	t.Cleanup(func() { cancel(); s.closeForTest() })
	return cc, s, clock, cancel
}

func TestQBlockCapabilityProbeEndpointFeedback(t *testing.T) {
	cc, s, clock, _ := capabilityEndpointConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	m := cc.qblockClient.endpoint
	require.False(t, m.admit(99, qblockProbeControl, 0, clock.Now()))
	wrong := capabilityReply(cc, sent)
	wrong.SetToken([]byte{99})
	ingestCapability(t, cc, wrong)
	require.False(t, m.admit(99, qblockProbeControl, 0, clock.Now()), "wrong token cannot release probe debt")
	ingestCapability(t, cc, capabilityReply(cc, sent))
	got := awaitCapability(t, result)
	require.NoError(t, got.err)
	require.True(t, got.supported)
	require.True(t, m.admit(99, qblockProbeControl, 0, clock.Now()), "valid probe response must release unanswered endpoint debt")
	require.Zero(t, cc.qblockClient.active())
}

func TestQBlockCapabilityProbeLifecycle(t *testing.T) {
	t.Run("cancel_releases_slot_and_does_not_cache", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		ctx, cancel := context.WithCancel(context.Background())
		result, _ := startCapabilityProbe(t, cc, s, ctx)
		_, err := cc.ProbeQBlock(context.Background(), "")
		require.ErrorIs(t, err, ErrQBlockProbeInProgress)
		cancel()
		require.ErrorIs(t, awaitCapability(t, result).err, context.Canceled)
		require.Zero(t, cc.tokenReservations.Length())
		require.Zero(t, cc.midHandlerContainer.Length())
		for range 2 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			ingestCapability(t, cc, capabilityReply(cc, sent))
			require.True(t, awaitCapability(t, result).supported)
		}
		require.Len(t, s.writesSnapshot(), 3)
	})
	t.Run("send_failure", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		s.writeErr = errors.New("send failure")
		ok, err := cc.ProbeQBlock(context.Background(), "")
		require.False(t, ok)
		require.ErrorIs(t, err, s.writeErr)
		require.Zero(t, cc.tokenReservations.Length())
		require.Zero(t, cc.midHandlerContainer.Length())
	})
	t.Run("token_collision", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		cc.getToken = func() (message.Token, error) { return message.Token{7}, nil }
		require.NoError(t, cc.claimToken([]byte{7}, tokenOwnerRequest))
		_, err := cc.ProbeQBlock(context.Background(), "")
		require.Error(t, err)
		require.Empty(t, s.writesSnapshot())
		require.Equal(t, 1, cc.tokenReservations.Length())
		cc.releaseToken([]byte{7}, tokenOwnerRequest)
	})
	t.Run("mid_collision", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		cc.msgID.Store(100)
		old := &midElement{}
		cc.midHandlerContainer.Store(101, old)
		_, err := cc.ProbeQBlock(context.Background(), "")
		require.Error(t, err)
		require.Empty(t, s.writesSnapshot())
		got, ok := cc.midHandlerContainer.Load(101)
		require.True(t, ok)
		require.Same(t, old, got)
		require.Zero(t, cc.tokenReservations.Length())
		cc.midHandlerContainer.Delete(101)
	})
	t.Run("datagram_budget", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		cc.qblockProbeLimit = 16
		_, err := cc.ProbeQBlock(context.Background(), "")
		require.Error(t, err)
		require.Empty(t, s.writesSnapshot())
		require.Zero(t, cc.tokenReservations.Length())
		_, err = cc.ProbeQBlock(context.Background(), "/"+strings.Repeat("x", 10000))
		require.Error(t, err)
		require.Empty(t, s.writesSnapshot())
	})
	t.Run("retransmit_and_expire", func(t *testing.T) {
		cc, s, _ := capabilityConn(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		result, sent := startCapabilityProbe(t, cc, s, ctx)
		cc.CheckExpirations(time.Now().Add(3 * time.Second))
		writes := s.writesSnapshot()
		require.Len(t, writes, 2)
		require.Equal(t, sent.mid, writes[1].mid)
		require.Equal(t, sent.token, writes[1].token)
		require.Equal(t, message.Confirmable, writes[1].typ)
		cc.CheckExpirations(time.Now().Add(2 * time.Minute))
		require.ErrorIs(t, awaitCapability(t, result).err, context.DeadlineExceeded)
		require.Zero(t, cc.tokenReservations.Length())
		require.Zero(t, cc.midHandlerContainer.Length())
	})
	for _, wait := range []string{"nstart", "endpoint"} {
		t.Run("close_during_"+wait, func(t *testing.T) {
			cc, s, clock, closeConn := capabilityEndpointConn(t)
			m := cc.qblockClient.endpoint
			if wait == "nstart" {
				require.NoError(t, cc.acquireOutstandingInteraction(context.Background()))
				defer cc.releaseOutstandingInteraction()
			} else {
				require.True(t, m.admit(99, qblockProbeControl, 0, clock.Now()))
				require.True(t, m.beginAttempt(99, 2))
				m.endAttempt(99, clock.Now())
				m.settle(99, clock.Now())
			}
			result := make(chan capabilityResult, 1)
			go func() { ok, err := cc.ProbeQBlock(context.Background(), ""); result <- capabilityResult{ok, err} }()
			require.Eventually(t, func() bool { return cc.tokenReservations.Length() == 1 }, time.Second, time.Millisecond)
			if wait == "endpoint" {
				require.Eventually(t, func() bool {
					m.domain.mu.Lock()
					defer m.domain.mu.Unlock()
					return len(m.domain.peers[m.peer].waiters) == 1
				}, time.Second, time.Millisecond)
			}
			closeConn()
			got := awaitCapability(t, result)
			require.Error(t, got.err)
			require.False(t, got.supported)
			require.Empty(t, s.writesSnapshot())
			require.Zero(t, cc.tokenReservations.Length())
			require.Zero(t, cc.midHandlerContainer.Length())
		})
	}
}

func TestQBlockCapabilityProbeMalformedEmptyACK(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	bad := cc.AcquireMessage(cc.Context())
	bad.SetType(message.Acknowledgement)
	bad.SetCode(codes.Empty)
	bad.SetMessageID(sent.mid)
	bad.SetToken(sent.token)
	ingestCapability(t, cc, bad)
	require.Equal(t, 1, cc.midHandlerContainer.Length())
	cc.CheckExpirations(time.Now().Add(3 * time.Second))
	require.Len(t, s.writesSnapshot(), 2)
	r := capabilityReply(cc, sent)
	r.SetType(message.NonConfirmable)
	r.SetMessageID(sent.mid + 22)
	ingestCapability(t, cc, r)
	require.True(t, awaitCapability(t, result).supported)
}

func TestQBlockCapabilityProbeOwnedBudget(t *testing.T) {
	cc, s, _, _ := capabilityEndpointConn(t)
	b := cc.qblockClient.ownedBudget
	release, err := b.acquire(b.limit - b.floor)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = cc.ProbeQBlock(ctx, "")
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	require.Empty(t, s.writesSnapshot(), "owned-copy admission must precede probe transmission")
	release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	result, sent := startCapabilityProbe(t, cc, s, ctx2)
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	require.Greater(t, used, b.floor)
	ingestCapability(t, cc, capabilityReply(cc, sent))
	require.True(t, awaitCapability(t, result).supported)
	b.mu.Lock()
	used = b.used
	b.mu.Unlock()
	require.Equal(t, b.floor, used)
}
