package client

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestMidElementRetransmitExponentialBackoff(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 100 * time.Millisecond,
	}

	require.False(t, m.Retransmit(start.Add(100*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(101*time.Millisecond)))
	m.MarkRetransmitted(start.Add(101 * time.Millisecond))
	require.False(t, m.Retransmit(start.Add(300*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(301*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(302*time.Millisecond)))
	m.MarkRetransmitted(start.Add(302 * time.Millisecond))
	require.False(t, m.Retransmit(start.Add(701*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(702*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(703*time.Millisecond)))
}

func TestMidElementRetransmitDelayedExpirationCheck(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 2500 * time.Millisecond,
	}

	retransmittedAt := start.Add(4 * time.Second)
	require.True(t, m.Retransmit(retransmittedAt))
	m.MarkRetransmitted(retransmittedAt)
	require.False(t, m.Retransmit(start.Add(8*time.Second)))
	require.False(t, m.Retransmit(start.Add(9*time.Second)))
	require.True(t, m.Retransmit(start.Add(9*time.Second+time.Millisecond)))
}

func TestTransmissionNextAcknowledgeTimeoutRandomFactor(t *testing.T) {
	transmission := Transmission{
		acknowledgeTimeout:      atomic.NewDuration(100 * time.Millisecond),
		acknowledgeRandomFactor: atomic.NewFloat64(1.5),
	}

	for range 100 {
		timeout := transmission.nextAcknowledgeTimeout()
		require.GreaterOrEqual(t, timeout, 100*time.Millisecond)
		require.Less(t, timeout, 150*time.Millisecond)
	}
}

func TestTransmissionNextAcknowledgeTimeoutInvalidFactor(t *testing.T) {
	for _, factor := range []float64{math.NaN(), math.Inf(1)} {
		transmission := Transmission{
			acknowledgeTimeout:      atomic.NewDuration(100 * time.Millisecond),
			acknowledgeRandomFactor: atomic.NewFloat64(factor),
		}

		require.Equal(t, 100*time.Millisecond, transmission.nextAcknowledgeTimeout())
	}
}

func TestTransmissionNextAcknowledgeTimeoutDurationLimit(t *testing.T) {
	transmission := Transmission{
		acknowledgeTimeout:      atomic.NewDuration(time.Duration(math.MaxInt64)),
		acknowledgeRandomFactor: atomic.NewFloat64(2),
	}

	require.Equal(t, time.Duration(math.MaxInt64), transmission.nextAcknowledgeTimeout())
}
