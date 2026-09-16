package qblock

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationKeyIsCanonicalAndOwnsParts(t *testing.T) {
	left, err := NewOperationKey([]byte("ab"), []byte("c"))
	require.NoError(t, err)
	right, err := NewOperationKey([]byte("a"), []byte("bc"))
	require.NoError(t, err)
	require.NotEqual(t, left, right)

	part := []byte("request-tag")
	key, err := NewOperationKey(part)
	require.NoError(t, err)
	part[0] = 'X'
	expected, err := NewOperationKey([]byte("request-tag"))
	require.NoError(t, err)
	require.Equal(t, expected, key)
}

func TestOperationKeyRejectsInvalidPartLists(t *testing.T) {
	_, err := NewOperationKey()
	require.Error(t, err)
	_, err = NewOperationKey([]byte{})
	require.Error(t, err)

	parts := make([][]byte, 33)
	for i := range parts {
		parts[i] = []byte("x")
	}
	_, err = NewOperationKey(parts...)
	require.Error(t, err)
	_, err = NewOperationKey(bytes.Repeat([]byte{'x'}, 513))
	require.Error(t, err)
}
