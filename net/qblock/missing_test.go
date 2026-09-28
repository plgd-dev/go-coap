package qblock

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMissingSequence(t *testing.T) {
	got, err := DecodeMissing([]byte{1, 1, 0x18, 24}, 25, 3)
	require.NoError(t, err)
	require.Equal(t, []uint32{1, 24}, got)
	payload, consumed, err := EncodeMissing([]uint32{1, 24}, 2)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, payload)
	require.Equal(t, 1, consumed)
}

func TestMissingUnsignedBoundaries(t *testing.T) {
	numbers := []uint32{23, 24, 255, 256, 65535, 65536}
	want := []byte{0x17, 0x18, 0x18, 0x18, 0xff, 0x19, 0x01, 0x00, 0x19, 0xff, 0xff, 0x1a, 0x00, 0x01, 0x00, 0x00}
	encoded, consumed, err := EncodeMissing(numbers, len(want))
	require.NoError(t, err)
	require.Equal(t, len(numbers), consumed)
	require.Equal(t, want, encoded)
	decoded, err := DecodeMissing(want, 65537, len(numbers))
	require.NoError(t, err)
	require.Equal(t, numbers, decoded)

	// A valid wider-than-necessary unsigned encoding is accepted.
	decoded, err = DecodeMissing([]byte{0x1b, 0, 0, 0, 0, 0, 0, 0, 1}, 2, 1)
	require.NoError(t, err)
	require.Equal(t, []uint32{1}, decoded)
}

func TestMissingRejectsInvalidSequences(t *testing.T) {
	for name, wire := range map[string][]byte{
		"empty":           {},
		"array wrapper":   {0x81, 1},
		"negative":        {0x20},
		"tag":             {0xc0, 1},
		"truncated":       {0x19, 0x01},
		"reserved":        {0x1f},
		"descending":      {2, 1},
		"out of body":     {2},
		"uint64 overflow": {0x1b, 0, 0, 0, 1, 0, 0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeMissing(wire, 2, 8)
			require.Error(t, err)
		})
	}
	_, err := DecodeMissing([]byte{1, 1}, 2, 1)
	require.Error(t, err)
	_, err = DecodeMissing([]byte{0}, 1, 0)
	require.Error(t, err)
}

func TestMissingEncoderLimits(t *testing.T) {
	for _, numbers := range [][]uint32{nil, {2, 1}, {1, 1}} {
		_, _, err := EncodeMissing(numbers, 10)
		require.Error(t, err)
	}
	_, _, err := EncodeMissing([]uint32{24}, 1)
	require.Error(t, err)
	_, _, err = EncodeMissing([]uint32{0}, 0)
	require.Error(t, err)
	encoded, consumed, err := EncodeMissing([]uint32{23, 24, 256}, 3)
	require.NoError(t, err)
	require.Equal(t, []byte{0x17, 0x18, 0x18}, encoded)
	require.Equal(t, 2, consumed)
}

func FuzzDecodeMissing(f *testing.F) {
	for _, seed := range [][]byte{{1, 1, 0x18, 24}, {0x17, 0x18, 0x18}, {0x81, 1}, {0x1b, 0, 0, 0, 1, 0, 0, 0, 0}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		got, err := DecodeMissing(wire, 1000, 100)
		if err != nil {
			return
		}
		for i, number := range got {
			require.Less(t, number, uint32(1000))
			if i > 0 {
				require.Greater(t, number, got[i-1])
			}
		}
	})
}
