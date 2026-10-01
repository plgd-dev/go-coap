package client

import (
	"context"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// Missing discovery transitions or cached explicit calls break this test.
func TestQBlockSessionKnowledge(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	require.Equal(t, qblockCapabilityUnknown, cc.qblockCapabilityState())
	for _, outcome := range []string{"positive", "qless", "badoption", "positive"} {
		result, sent := startCapabilityProbe(t, cc, s, context.Background())
		reply := capabilityReply(cc, sent)
		want := qblockCapabilitySupported
		if outcome == "qless" {
			reply.ResetOptionsTo(nil)
		}
		if outcome == "badoption" {
			reply.SetCode(codes.BadOption)
			reply.ResetOptionsTo(nil)
			reply.SetBody(nil)
			want = qblockCapabilityUnsupported
		}
		ingestCapability(t, cc, reply)
		got := awaitCapability(t, result)
		require.NoError(t, got.err)
		require.Equal(t, outcome == "positive", got.supported)
		require.Equal(t, want, cc.qblockCapabilityState())
	}
	require.Len(t, s.writesSnapshot(), 4)
	other, _, _ := capabilityConn(t)
	require.Equal(t, qblockCapabilityUnknown, other.qblockCapabilityState())
}

// Exclusive busy rejection or first-caller context ownership breaks sharing.
func TestQBlockSessionCoalescing(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	leaderCtx, cancel := context.WithCancel(context.Background())
	leader, sent := startCapabilityProbe(t, cc, s, leaderCtx)
	follower := make(chan capabilityResult, 1)
	go func() {
		ok, err := cc.ProbeQBlock(context.Background(), "/.well-known/core")
		follower <- capabilityResult{ok, err}
	}()
	require.Eventually(t, func() bool {
		cc.qblockProbeMu.Lock()
		defer cc.qblockProbeMu.Unlock()
		return cc.qblockGeneration != nil && cc.qblockGeneration.waiters == 2
	}, time.Second, time.Millisecond)
	_, err := cc.ProbeQBlock(context.Background(), "/different")
	require.ErrorIs(t, err, ErrQBlockProbeInProgress)
	cancel()
	require.ErrorIs(t, awaitCapability(t, leader).err, context.Canceled)
	ingestCapability(t, cc, capabilityReply(cc, sent))
	require.True(t, awaitCapability(t, follower).supported)
	require.Len(t, s.writesSnapshot(), 1)
}
func TestQBlockSessionWaiterLimit(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader, _ := startCapabilityProbe(t, cc, s, ctx)
	results := make(chan capabilityResult, 63)
	for range 63 {
		go func() { ok, err := cc.ProbeQBlock(ctx, ""); results <- capabilityResult{ok, err} }()
	}
	require.Eventually(t, func() bool {
		cc.qblockProbeMu.Lock()
		defer cc.qblockProbeMu.Unlock()
		return cc.qblockGeneration != nil && cc.qblockGeneration.waiters == 64
	}, time.Second, time.Millisecond)
	_, err := cc.ProbeQBlock(context.Background(), "")
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	cancel()
	require.ErrorIs(t, awaitCapability(t, leader).err, context.Canceled)
	for range 63 {
		require.ErrorIs(t, awaitCapability(t, results).err, context.Canceled)
	}
	require.Zero(t, cc.tokenReservations.Length())
	require.Zero(t, cc.midHandlerContainer.Length())
}

// Late/conflicting publication must not revoke frozen knowledge or own successors.
func TestQBlockSessionTerminalOwnership(t *testing.T) {
	for _, scenario := range []string{"conflict", "expired", "closed", "departed"} {
		t.Run(scenario, func(t *testing.T) {
			cc, s, closeConn := capabilityConn(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			cc.qblockProbeMu.Lock()
			g := cc.qblockGeneration
			cc.qblockProbeMu.Unlock()
			if scenario == "expired" {
				cc.qblockProbeMu.Lock()
				g.deadline = time.Now().Add(-time.Second)
				cc.qblockProbeMu.Unlock()
			}
			if scenario == "closed" {
				closeConn()
			}
			if scenario == "departed" {
				cancel()
				require.ErrorIs(t, awaitCapability(t, result).err, context.Canceled)
			}
			if scenario != "departed" {
				ingestCapability(t, cc, capabilityReply(cc, sent))
				got := awaitCapability(t, result)
				if scenario == "conflict" {
					require.True(t, got.supported)
				} else {
					require.Error(t, got.err)
				}
			}
			cc.publishQBlockProbe(g, false, nil, qblockCapabilityUnsupported)
			want := qblockCapabilityUnknown
			if scenario == "conflict" {
				want = qblockCapabilitySupported
			}
			require.Equal(t, want, cc.qblockCapabilityState())
			require.Zero(t, cc.tokenReservations.Length())
			require.Zero(t, cc.midHandlerContainer.Length())
		})
	}
}

func TestQBlockSessionCleanupAdmission(t *testing.T) {
	cc, _, _ := capabilityConn(t)
	cc.qblockProbeMu.Lock()
	ctx, cancel := context.WithCancel(cc.Context())
	defer cancel()
	g := &qblockProbeGeneration{path: "/.well-known/core", context: ctx, cancel: cancel, deadline: time.Now().Add(time.Minute), waiters: 1, done: make(chan struct{}), cleaned: make(chan struct{})}
	cc.qblockGeneration = g
	cc.freezeQBlockProbeLocked(g, true, nil, qblockCapabilitySupported)
	cc.qblockProbeMu.Unlock()
	for _, path := range []string{"", "/different"} {
		_, err := cc.ProbeQBlock(context.Background(), path)
		require.ErrorIs(t, err, ErrQBlockProbeInProgress)
	}
	cc.qblockProbeMu.Lock()
	cc.qblockGeneration = nil
	cc.qblockProbeMu.Unlock()
	// A retired publisher must not change the successor's unknown state.
	cc.qblockProbeMu.Lock()
	cc.qblockKnowledge = qblockCapabilityUnknown
	successor := &qblockProbeGeneration{}
	cc.qblockGeneration = successor
	cc.qblockProbeMu.Unlock()
	cc.publishQBlockProbe(g, true, nil, qblockCapabilitySupported)
	require.Equal(t, qblockCapabilityUnknown, cc.qblockCapabilityState())
	cc.qblockProbeMu.Lock()
	require.Same(t, successor, cc.qblockGeneration)
	cc.qblockGeneration = nil
	cc.qblockProbeMu.Unlock()
}

func TestQBlockProbeUnpublishesBeforeLeaseRelease(t *testing.T) {
	cc, s, _, _ := capabilityEndpointConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	result, _ := startCapabilityProbe(t, cc, s, ctx)
	cc.qblockProbeMu.Lock()
	p := cc.qblockProbe
	cc.qblockProbeMu.Unlock()
	entered, resume := make(chan struct{}), make(chan struct{})
	p.lease.mu.Lock()
	release := p.lease.release
	p.lease.release = func() { release(); close(entered); <-resume }
	p.lease.mu.Unlock()
	cancel()
	<-entered
	cc.qblockProbeMu.Lock()
	published := cc.qblockProbe != nil
	cc.qblockProbeMu.Unlock()
	close(resume)
	require.ErrorIs(t, awaitCapability(t, result).err, context.Canceled)
	require.False(t, published, "lease release must follow ingress unpublication")
}
