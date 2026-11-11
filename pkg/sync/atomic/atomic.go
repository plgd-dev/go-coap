package atomic

import (
	"encoding/json"
	"math"
	"sync/atomic"
	"time"
	"unsafe"
)

type (
	Bool           = atomic.Bool
	Int32          = atomic.Int32
	Int64          = atomic.Int64
	Uint32         = atomic.Uint32
	Uint64         = atomic.Uint64
	Value          = atomic.Value
	Pointer[T any] = atomic.Pointer[T]
)

func NewBool(value bool) *Bool {
	v := new(Bool)
	v.Store(value)
	return v
}

func NewInt32(value int32) *Int32 {
	v := new(Int32)
	v.Store(value)
	return v
}

func NewInt64(value int64) *Int64 {
	v := new(Int64)
	v.Store(value)
	return v
}

func NewUint32(value uint32) *Uint32 {
	v := new(Uint32)
	v.Store(value)
	return v
}

func NewUint64(value uint64) *Uint64 {
	v := new(Uint64)
	v.Store(value)
	return v
}

type Duration struct {
	value atomic.Int64
}

func NewDuration(value time.Duration) *Duration {
	v := new(Duration)
	v.Store(value)
	return v
}

func (v *Duration) Load() time.Duration {
	return time.Duration(v.value.Load())
}

func (v *Duration) Store(value time.Duration) {
	v.value.Store(int64(value))
}

func (v *Duration) Add(delta time.Duration) time.Duration {
	return time.Duration(v.value.Add(int64(delta)))
}

func (v *Duration) Sub(delta time.Duration) time.Duration {
	return v.Add(-delta)
}

func (v *Duration) CompareAndSwap(old, newValue time.Duration) bool {
	return v.value.CompareAndSwap(int64(old), int64(newValue))
}

func (v *Duration) CAS(old, newValue time.Duration) bool {
	return v.CompareAndSwap(old, newValue)
}

func (v *Duration) Swap(value time.Duration) time.Duration {
	return time.Duration(v.value.Swap(int64(value)))
}

func (v *Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Load())
}

func (v *Duration) UnmarshalJSON(data []byte) error {
	var value time.Duration
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Store(value)
	return nil
}

type Float64 struct {
	value atomic.Uint64
}

func NewFloat64(value float64) *Float64 {
	v := new(Float64)
	v.Store(value)
	return v
}

func (v *Float64) Load() float64 {
	return math.Float64frombits(v.value.Load())
}

func (v *Float64) Store(value float64) {
	v.value.Store(math.Float64bits(value))
}

func (v *Float64) Add(delta float64) float64 {
	for {
		old := v.value.Load()
		newValue := math.Float64bits(math.Float64frombits(old) + delta)
		if v.value.CompareAndSwap(old, newValue) {
			return math.Float64frombits(newValue)
		}
	}
}

func (v *Float64) Sub(delta float64) float64 {
	return v.Add(-delta)
}

func (v *Float64) CompareAndSwap(old, newValue float64) bool {
	return v.value.CompareAndSwap(math.Float64bits(old), math.Float64bits(newValue))
}

func (v *Float64) CAS(old, newValue float64) bool {
	return v.CompareAndSwap(old, newValue)
}

func (v *Float64) Swap(value float64) float64 {
	return math.Float64frombits(v.value.Swap(math.Float64bits(value)))
}

func (v *Float64) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Load())
}

func (v *Float64) UnmarshalJSON(data []byte) error {
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Store(value)
	return nil
}

type Time struct {
	value atomic.Pointer[time.Time]
}

func NewTime(value time.Time) *Time {
	v := new(Time)
	v.Store(value)
	return v
}

func (v *Time) Load() time.Time {
	value := v.value.Load()
	if value == nil {
		return time.Time{}
	}
	return *value
}

func (v *Time) Store(value time.Time) {
	v.value.Store(&value)
}

func (v *Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Load())
}

func (v *Time) UnmarshalJSON(data []byte) error {
	var value time.Time
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	v.Store(value)
	return nil
}

type UnsafePointer struct {
	value atomic.Pointer[byte]
}

func NewUnsafePointer(value unsafe.Pointer) *UnsafePointer {
	v := new(UnsafePointer)
	v.Store(value)
	return v
}

func (v *UnsafePointer) Load() unsafe.Pointer {
	return unsafe.Pointer(v.value.Load())
}

func (v *UnsafePointer) Store(value unsafe.Pointer) {
	v.value.Store((*byte)(value))
}

func (v *UnsafePointer) Swap(value unsafe.Pointer) unsafe.Pointer {
	return unsafe.Pointer(v.value.Swap((*byte)(value)))
}

func (v *UnsafePointer) CompareAndSwap(old, newValue unsafe.Pointer) bool {
	return v.value.CompareAndSwap((*byte)(old), (*byte)(newValue))
}
