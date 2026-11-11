package atomic

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDuration(t *testing.T) {
	var value Duration
	require.Zero(t, value.Load())
	require.Equal(t, 3*time.Second, value.Add(3*time.Second))
	require.True(t, value.CompareAndSwap(3*time.Second, time.Second))
	require.Equal(t, time.Second, value.Swap(2*time.Second))
	require.Equal(t, 2*time.Second, value.Load())
}

func TestFloat64(t *testing.T) {
	value := NewFloat64(1.5)
	require.InDelta(t, 3.5, value.Add(2), 1e-12)
	require.True(t, value.CompareAndSwap(3.5, 4))
	require.InDelta(t, 3.0, value.Sub(1), 1e-12)
	require.InDelta(t, 3.0, value.Load(), 1e-12)
	require.InDelta(t, 3.0, value.Swap(math.Pi), 1e-12)
	require.InDelta(t, math.Pi, value.Load(), 1e-12)
}

func TestTime(t *testing.T) {
	var value Time
	require.True(t, value.Load().IsZero())

	want := time.Now()
	value.Store(want)
	require.True(t, value.Load().Equal(want))

	data, err := json.Marshal(&value)
	require.NoError(t, err)
	var decoded Time
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.True(t, decoded.Load().Equal(want))
}
