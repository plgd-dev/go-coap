package math

import (
	"fmt"
	"log"
	"reflect"
	"unsafe"

	"golang.org/x/exp/constraints"
)

func Max[T constraints.Integer]() T {
	size := unsafe.Sizeof(T(0))
	switch reflect.TypeOf((*T)(nil)).Elem().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return T(1<<(size*8-1) - 1) // 2^(n-1) - 1 for signed integers
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return T(1<<(size*8) - 1) // 2^n - 1 for unsigned integers
	default:
		panic("unsupported type")
	}
}

func Min[T constraints.Integer]() T {
	size := unsafe.Sizeof(T(0))
	switch reflect.TypeOf((*T)(nil)).Elem().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return T(int64(-1) << (size*8 - 1)) // -2^(n-1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return T(0)
	default:
		panic("unsupported type")
	}
}

func SafeCastTo[T, F constraints.Integer](from F) (T, error) {
	if from > 0 && uint64(Max[T]()) < uint64(from) {
		return T(0), fmt.Errorf("value(%v) exceeds the maximum value for type(%v)", from, Max[T]())
	}
	if from < 0 && int64(Min[T]()) > int64(from) {
		return T(0), fmt.Errorf("value(%v) exceeds the minimum value for type(%v)", from, Min[T]())
	}
	return T(from), nil
}

func CastTo[T, F constraints.Integer](from F) T {
	return T(from)
}

func MustSafeCastTo[T, F constraints.Integer](from F) T {
	to, err := SafeCastTo[T](from)
	if err != nil {
		log.Panicf("value (%v) out of bounds for type %T", from, T(0))
	}
	return to
}
