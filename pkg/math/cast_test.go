package math_test

import (
	"math"
	"testing"

	pkgMath "github.com/plgd-dev/go-coap/v3/pkg/math"
	"github.com/stretchr/testify/require"
)

func TestCastToUint8(t *testing.T) {
	// uint8
	_, err := pkgMath.SafeCastTo[uint8](uint8(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[uint8](math.MaxUint8)
	require.NoError(t, err)
	// int8
	_, err = pkgMath.SafeCastTo[uint8](math.MinInt8)
	require.Error(t, err)
	_, err = pkgMath.SafeCastTo[uint8](int8(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[uint8](math.MaxInt8)
	require.NoError(t, err)
	// uint64
	_, err = pkgMath.SafeCastTo[uint8](uint64(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[uint8](uint64(math.MaxUint8))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[uint8](uint64(math.MaxUint64))
	require.Error(t, err)
	// int64
	_, err = pkgMath.SafeCastTo[uint8](math.MaxInt64)
	require.Error(t, err)
	_, err = pkgMath.SafeCastTo[uint8](int64(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[uint8](math.MaxInt64)
	require.Error(t, err)
	_, err = pkgMath.SafeCastTo[uint8](int64(math.MaxUint8))
	require.NoError(t, err)
}

func TestCastToInt8(t *testing.T) {
	// uint8
	_, err := pkgMath.SafeCastTo[int8](uint8(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[int8](math.MaxUint8)
	require.Error(t, err)
	// int8
	_, err = pkgMath.SafeCastTo[int8](math.MinInt8)
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[int8](math.MaxInt8)
	require.NoError(t, err)
	// uint64
	_, err = pkgMath.SafeCastTo[int8](uint64(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[int8](uint64(math.MaxInt8))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[int8](uint64(math.MaxUint64))
	require.Error(t, err)
	// int64
	_, err = pkgMath.SafeCastTo[int8](math.MaxInt64)
	require.Error(t, err)
	_, err = pkgMath.SafeCastTo[int8](int64(0))
	require.NoError(t, err)
	_, err = pkgMath.SafeCastTo[int8](math.MaxInt64)
	require.Error(t, err)
	_, err = pkgMath.SafeCastTo[int8](int64(math.MaxInt8))
	require.NoError(t, err)
}
