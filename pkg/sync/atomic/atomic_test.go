package atomic

import (
	"encoding/json"
	"math"
	"sync"
	stdatomic "sync/atomic"
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

func TestFloat64CompareAndSwapBitPatterns(t *testing.T) {
	tests := []struct {
		name        string
		initialBits uint64
		oldBits     uint64
		newBits     uint64
		wantSwap    bool
	}{
		{
			name:        "positive zero does not match negative zero",
			initialBits: math.Float64bits(0),
			oldBits:     math.Float64bits(math.Copysign(0, -1)),
			newBits:     math.Float64bits(1),
		},
		{
			name:        "positive zero matches positive zero",
			initialBits: math.Float64bits(0),
			oldBits:     math.Float64bits(0),
			newBits:     math.Float64bits(math.Copysign(0, -1)),
			wantSwap:    true,
		},
		{
			name:        "same NaN payload matches",
			initialBits: 0x7ff8000000000001,
			oldBits:     0x7ff8000000000001,
			newBits:     math.Float64bits(1),
			wantSwap:    true,
		},
		{
			name:        "different NaN payload does not match",
			initialBits: 0x7ff8000000000001,
			oldBits:     0x7ff8000000000002,
			newBits:     math.Float64bits(1),
		},
		{
			name:        "ordinary value matches",
			initialBits: math.Float64bits(42),
			oldBits:     math.Float64bits(42),
			newBits:     math.Float64bits(43),
			wantSwap:    true,
		},
		{
			name:        "ordinary value mismatch",
			initialBits: math.Float64bits(42),
			oldBits:     math.Float64bits(41),
			newBits:     math.Float64bits(43),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := NewFloat64(math.Float64frombits(tt.initialBits))
			swapped := value.CompareAndSwap(
				math.Float64frombits(tt.oldBits),
				math.Float64frombits(tt.newBits),
			)
			require.Equal(t, tt.wantSwap, swapped)
			if tt.wantSwap {
				require.Equal(t, tt.newBits, math.Float64bits(value.Load()))
			} else {
				require.Equal(t, tt.initialBits, math.Float64bits(value.Load()))
			}
		})
	}
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

func TestAtomicTypesConcurrentOperations(t *testing.T) {
	const (
		workers    = 8
		iterations = 1000
	)

	t.Run("Duration", func(t *testing.T) {
		value := NewDuration(0)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(3)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					value.Add(time.Nanosecond)
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					value.Sub(time.Nanosecond)
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					for {
						old := value.Load()
						if value.CompareAndSwap(old, old+time.Nanosecond) {
							break
						}
					}
				}
			}()
		}
		wg.Wait()
		require.Equal(t, time.Duration(workers*iterations)*time.Nanosecond, value.Load())
	})

	t.Run("Float64", func(t *testing.T) {
		value := NewFloat64(0)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(3)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					value.Add(0.25)
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					value.Sub(0.25)
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					for {
						old := value.Load()
						if value.CompareAndSwap(old, old+0.5) {
							break
						}
					}
				}
			}()
		}
		wg.Wait()
		require.Equal(
			t,
			math.Float64bits(float64(workers*iterations)*0.5),
			math.Float64bits(value.Load()),
		)
	})

	t.Run("Time", func(t *testing.T) {
		values := []time.Time{
			time.Unix(1, 1),
			time.Unix(2, 2),
			time.Unix(3, 3),
		}
		value := NewTime(values[0])
		var wg sync.WaitGroup
		var invalid Bool
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < workers*iterations; i++ {
				value.Store(values[i%len(values)])
			}
		}()
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					loaded := value.Load()
					valid := false
					for _, candidate := range values {
						if loaded.Equal(candidate) {
							valid = true
							break
						}
					}
					if !valid {
						invalid.Store(true)
						return
					}
				}
			}()
		}
		wg.Wait()
		require.False(t, invalid.Load(), "Load returned a torn or unexpected time")
	})

	t.Run("UnsafePointer", func(t *testing.T) {
		type pointedValue struct {
			value int
		}
		values := []*pointedValue{{value: 1}, {value: 2}, {value: 3}}
		pointers := []unsafe.Pointer{
			unsafe.Pointer(values[0]),
			unsafe.Pointer(values[1]),
			unsafe.Pointer(values[2]),
		}
		value := NewUnsafePointer(pointers[0])
		var wg sync.WaitGroup
		var invalid Bool
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					next := pointers[(worker+j)%len(pointers)]
					value.Store(next)
					old := value.Swap(next)
					if !containsPointer(pointers, old) {
						invalid.Store(true)
					}
					current := value.Load()
					if !containsPointer(pointers, current) {
						invalid.Store(true)
					}
					value.CompareAndSwap(current, next)
				}
			}(i)
		}
		wg.Wait()
		require.False(t, invalid.Load(), "pointer operation returned an unknown pointer")
		require.Contains(t, pointers, value.Load())
	})
}

func containsPointer(values []unsafe.Pointer, value unsafe.Pointer) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
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

var benchmarkTimeSink time.Time

// The AtomicValueReference benchmarks mirror the storage used by go.uber.org/atomic.Time.
func BenchmarkTimeStore(b *testing.B) {
	want := time.Date(2026, time.November, 11, 0, 0, 0, 0, time.UTC)
	b.Run("PointerAtomic", func(b *testing.B) {
		value := NewTime(want)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			value.Store(want)
		}
	})
	b.Run("AtomicValueReference", func(b *testing.B) {
		var value stdatomic.Value
		value.Store(want)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			value.Store(want)
		}
	})
}

func BenchmarkTimeLoad(b *testing.B) {
	want := time.Date(2026, time.November, 11, 0, 0, 0, 0, time.UTC)
	b.Run("PointerAtomic", func(b *testing.B) {
		value := NewTime(want)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchmarkTimeSink = value.Load()
		}
	})
	b.Run("AtomicValueReference", func(b *testing.B) {
		var value stdatomic.Value
		value.Store(want)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchmarkTimeSink = value.Load().(time.Time)
		}
	})
}
