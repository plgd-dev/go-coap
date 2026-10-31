package client

import (
	"testing"
	"time"

	"go.uber.org/atomic"

	"github.com/stretchr/testify/require"
)

func TestMidElementRetransmitExponentialBackoff(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 100 * time.Millisecond,
	}

	require.False(t, m.Retransmit(start.Add(100*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(101*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(300*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(301*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(700*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(701*time.Millisecond)))
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
