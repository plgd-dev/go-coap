package qblock

import (
	"math"
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

func TestMissingIntegerBoundaries(t *testing.T) {
	vectors := []struct {
		number uint32
		wire   []byte
	}{
		{23, []byte{0x17}},
		{24, []byte{0x18, 0x18}},
		{255, []byte{0x18, 0xff}},
		{256, []byte{0x19, 0x01, 0x00}},
		{65535, []byte{0x19, 0xff, 0xff}},
		{65536, []byte{0x1a, 0x00, 0x01, 0x00, 0x00}},
	}
	for _, tc := range vectors {
		payload, consumed, err := EncodeMissing([]uint32{tc.number}, len(tc.wire))
		require.NoError(t, err)
		require.Equal(t, tc.wire, payload)
		require.Equal(t, 1, consumed)

		got, err := DecodeMissing(tc.wire, tc.number+1, 1)
		require.NoError(t, err)
		require.Equal(t, []uint32{tc.number}, got)
	}

	got, err := DecodeMissing([]byte{0x1b, 0, 0, 0, 0, 0, 1, 0, 0}, 65537, 1)
	require.NoError(t, err)
	require.Equal(t, []uint32{65536}, got)
}

func TestDecodeMissingRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name       string
		wire       []byte
		blockCount uint32
		maxItems   int
	}{
		{"empty sequence", nil, 1, 1},
		{"zero item limit", []byte{0}, 1, 0},
		{"duplicate exceeds wire limit", []byte{1, 1}, 2, 1},
		{"descending", []byte{2, 1}, 3, 2},
		{"out of body", []byte{2}, 2, 1},
		{"array", []byte{0x81, 0}, 1, 2},
		{"negative", []byte{0x20}, 1, 1},
		{"tag", []byte{0xc0, 0}, 1, 2},
		{"truncated uint8", []byte{0x18}, 25, 1},
		{"truncated uint16", []byte{0x19, 1}, 257, 1},
		{"truncated uint32", []byte{0x1a, 0, 1, 0}, 65537, 1},
		{"truncated uint64", []byte{0x1b, 0, 0, 0, 0}, math.MaxUint32, 1},
		{"uint64 overflow", []byte{0x1b, 0, 0, 0, 1, 0, 0, 0, 0}, math.MaxUint32, 1},
		{"indefinite", []byte{0x1f}, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeMissing(tc.wire, tc.blockCount, tc.maxItems)
			require.Error(t, err)
		})
	}
}

func TestEncodeMissingRejectsInvalidInputBeforeTruncating(t *testing.T) {
	for _, tc := range []struct {
		name    string
		numbers []uint32
		budget  int
	}{
		{"empty input", nil, 1},
		{"zero budget", []uint32{0}, 0},
		{"negative budget", []uint32{0}, -1},
		{"duplicate after fitting prefix", []uint32{1, 1}, 1},
		{"descending after fitting prefix", []uint32{1, 24, 23}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := EncodeMissing(tc.numbers, tc.budget)
			require.Error(t, err)
		})
	}
}

func TestEncodeMissingBudgetExhaustion(t *testing.T) {
	_, _, err := EncodeMissing([]uint32{24}, 1)
	require.Error(t, err)

	payload, consumed, err := EncodeMissing([]uint32{23, 24, 255}, 3)
	require.NoError(t, err)
	require.Equal(t, []byte{0x17, 0x18, 0x18}, payload)
	require.Equal(t, 2, consumed)
}

func FuzzDecodeMissing(f *testing.F) {
	for _, seed := range [][]byte{
		{1, 1, 0x18, 24},
		{0x17, 0x18, 0x18, 0x18, 0xff},
		{0x19, 1, 0},
		{0x1b, 0, 0, 0, 1, 0, 0, 0, 0},
		{0x81, 0},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := DecodeMissing(data, 1024, 64)
		if err != nil {
			return
		}
		require.NotEmpty(t, got)
		for i, number := range got {
			require.Less(t, number, uint32(1024))
			if i > 0 {
				require.Less(t, got[i-1], number)
			}
		}
	})
}
