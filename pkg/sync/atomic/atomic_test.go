package atomic

import (
	"encoding/json"
	"math"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestPrimitiveConstructors(t *testing.T) {
	boolValue := NewBool(true)
	require.True(t, boolValue.Load())

	int32Value := NewInt32(32)
	require.Equal(t, int32(32), int32Value.Load())

	int64Value := NewInt64(64)
	require.Equal(t, int64(64), int64Value.Load())

	uint32Value := NewUint32(32)
	require.Equal(t, uint32(32), uint32Value.Load())

	uint64Value := NewUint64(64)
	require.Equal(t, uint64(64), uint64Value.Load())
}

func TestDuration(t *testing.T) {
	value := NewDuration(0)
	require.Zero(t, value.Load())
	require.Equal(t, 3*time.Second, value.Add(3*time.Second))
	require.True(t, value.CompareAndSwap(3*time.Second, time.Second))
	require.False(t, value.CompareAndSwap(3*time.Second, 4*time.Second))
	require.True(t, value.CAS(time.Second, 3*time.Second))
	require.Equal(t, 2*time.Second, value.Sub(time.Second))
	require.Equal(t, 2*time.Second, value.Swap(2*time.Second))
	require.Equal(t, 2*time.Second, value.Load())

	data, err := value.MarshalJSON()
	require.NoError(t, err)
	require.JSONEq(t, `2000000000`, string(data))
	var decoded Duration
	require.NoError(t, decoded.UnmarshalJSON([]byte(`3000000000`)))
	require.Equal(t, 3*time.Second, decoded.Load())
	require.Error(t, decoded.UnmarshalJSON([]byte(`"invalid"`)))
}

func TestFloat64(t *testing.T) {
	value := NewFloat64(1.5)
	require.InDelta(t, 3.5, value.Add(2), 1e-12)
	require.True(t, value.CompareAndSwap(3.5, 4))
	require.False(t, value.CompareAndSwap(3.5, 5))
	require.True(t, value.CAS(4, 3.5))
	require.InDelta(t, 3.0, value.Sub(0.5), 1e-12)
	require.InDelta(t, 3.0, value.Load(), 1e-12)
	require.InDelta(t, 3.0, value.Swap(math.Pi), 1e-12)
	require.InDelta(t, math.Pi, value.Load(), 1e-12)

	v, err := value.MarshalJSON()
	require.NoError(t, err)
	var decoded Float64
	require.NoError(t, json.Unmarshal(v, &decoded))
	require.InDelta(t, math.Pi, decoded.Load(), 1e-12)
	require.Error(t, decoded.UnmarshalJSON([]byte(`"invalid"`)))
}

func TestTime(t *testing.T) {
	value := NewTime(time.Time{})
	require.True(t, value.Load().IsZero())
	var zero Time
	require.True(t, zero.Load().IsZero())

	want := time.Now()
	value.Store(want)
	require.True(t, value.Load().Equal(want))

	data, err := json.Marshal(&value)
	require.NoError(t, err)
	var decoded Time
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.True(t, decoded.Load().Equal(want))
	require.Error(t, decoded.UnmarshalJSON([]byte(`"invalid"`)))
}

func TestUnsafePointer(t *testing.T) {
	type testStruct struct {
		value int
	}
	want := &testStruct{value: 42}
	value := NewUnsafePointer(unsafe.Pointer(want))
	require.Equal(t, unsafe.Pointer(want), value.Load())

	newValue := &testStruct{value: 43}
	require.True(t, value.CompareAndSwap(unsafe.Pointer(want), unsafe.Pointer(newValue)))
	require.Equal(t, unsafe.Pointer(newValue), value.Load())

	_ = value.Swap(unsafe.Pointer(want))
	require.Equal(t, unsafe.Pointer(want), value.Load())
}
