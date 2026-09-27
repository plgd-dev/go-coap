package client

import (
	"math"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

func TestQBlockPacingArithmetic(t *testing.T) {
	require.Equal(t, 333333334*time.Nanosecond, qblockProbeDelay(1, 3, 0))
	require.Equal(t, 100*time.Second, qblockProbeDelay(100, 1, 0))
	require.Equal(t, 7*time.Second, qblockProbeDelay(1000, 1, 7*time.Second))
	require.Equal(t, time.Duration(math.MaxInt64), qblockProbeDelay(math.MaxUint64, 1, 0))

	mc := qblock.DefaultManagerConfig()
	cfg, err := normalizeQBlockPacingConfig(nil, mc)
	require.NoError(t, err)
	require.Equal(t, uint64(1), cfg.ProbingRate)
	require.Equal(t, mc.MaxRetainedBytes, cfg.MaxIntentBytes)
	for _, tt := range []struct {
		jitter float64
		want   time.Duration
	}{{0, 247 * time.Second}, {1, 248 * time.Second}} {
		wait, err := qblockProbingWait(cfg, mc.Transfer, tt.jitter)
		require.NoError(t, err)
		require.Equal(t, tt.want, wait)
	}
	cfg.NonProbingWait = 9 * time.Second
	for _, jitter := range []float64{0, 1} {
		wait, err := qblockProbingWait(cfg, mc.Transfer, jitter)
		require.NoError(t, err)
		require.Equal(t, 9*time.Second, wait)
	}
	for _, bad := range []*qblockPacingConfig{
		{ProbingRate: 0, MaxIntentBytes: 1},
		{ProbingRate: 1, MaxIntentBytes: 0},
		{ProbingRate: 1, MaxIntentBytes: 1, NonProbingWait: -time.Second},
	} {
		_, err := normalizeQBlockPacingConfig(bad, mc)
		require.Error(t, err)
	}
	cfg.NonProbingWait = 0
	for _, jitter := range []float64{-0.1, 1.1, math.NaN(), math.Inf(1)} {
		_, err := qblockProbingWait(cfg, mc.Transfer, jitter)
		require.Error(t, err)
	}
	big := mc.Transfer
	big.NonTimeout = time.Hour
	big.NonReceiveTimeout = 2 * time.Hour
	big.NonMaxRetransmit = 30
	_, err = qblockProbingWait(cfg, big, 1)
	require.Error(t, err)
}

func TestQBlockPacingGateBodyAndControl(t *testing.T) {
	now := time.Unix(100, 0)
	g := newQBlockProbeGate(1)
	require.True(t, g.admit(1, qblockProbeBody, 7*time.Second, now))
	g.charge(1, 100)
	require.False(t, g.admit(2, qblockProbeControl, 0, now))
	g.settle(1, now)
	deadline, ok := g.nextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(7*time.Second), deadline)
	require.False(t, g.ready(now.Add(7*time.Second-time.Nanosecond)))
	require.True(t, g.ready(now.Add(7*time.Second)))
	require.True(t, g.admit(2, qblockProbeControl, 0, now.Add(7*time.Second)))
	g.charge(2, 100)
	g.settle(2, now.Add(7*time.Second))
	deadline, ok = g.nextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(107*time.Second), deadline)
	require.False(t, g.ready(now.Add(106*time.Second)))
	require.True(t, g.ready(now.Add(107*time.Second)))
	require.True(t, g.admit(3, qblockProbeControl, 0, now.Add(200*time.Second)))
	require.False(t, g.admit(4, qblockProbeControl, 0, now.Add(200*time.Second)))
}

func TestQBlockPacingGateStaleFeedbackAndCancellation(t *testing.T) {
	now := time.Unix(100, 0)
	g := newQBlockProbeGate(1)
	require.False(t, g.admit(0, qblockProbeBody, 7*time.Second, now))
	require.True(t, g.admit(1, qblockProbeBody, 7*time.Second, now))
	require.False(t, g.feedback(1))
	g.charge(1, 3)
	require.True(t, g.feedback(1))
	require.False(t, g.feedback(1))
	require.True(t, g.admit(2, qblockProbeControl, 0, now))
	require.False(t, g.feedback(1))
	g.settle(2, now)
	require.True(t, g.ready(now))
	require.True(t, g.admit(3, qblockProbeBody, 7*time.Second, now))
	g.charge(3, 100)
	g.settle(3, now)
	require.False(t, g.admit(4, qblockProbeBody, 7*time.Second, now))
	require.False(t, g.feedback(2))
	require.True(t, g.ready(now.Add(7*time.Second)))
}
