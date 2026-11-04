package qblock

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQBlockCodec(t *testing.T) {
	for szx := SZX16; szx <= SZX1024; szx++ {
		for _, num := range []int64{0, 1, 15, 16, 255, 256, 4095, 4096, 65535, 65536, 0xffff7, 0xffff8, 0xfffff} {
			for _, more := range []bool{false, true} {
				value, err := EncodeQBlockOption(szx, num, more)
				require.NoError(t, err)
				size, number, m, err := DecodeQBlockOption(value)
				require.NoError(t, err)
				require.Equal(t, szx, size)
				require.Equal(t, num, number)
				require.Equal(t, more, m)
			}
		}
	}
	for _, szx := range []SZX{SZXBERT, 8, 255} {
		_, err := EncodeQBlockOption(szx, 0, false)
		require.ErrorIs(t, err, ErrInvalidSZX)
	}
	for _, num := range []int64{-1, 1 << 20} {
		_, err := EncodeQBlockOption(SZX16, num, false)
		require.ErrorIs(t, err, ErrBlockNumberExceedLimit)
	}
	for _, value := range []uint32{7, 15, 0xffffff} {
		_, _, _, err := DecodeQBlockOption(value)
		require.ErrorIs(t, err, ErrInvalidSZX)
	}
	for _, value := range []uint32{1 << 24, 0xffffffff} {
		_, _, _, err := DecodeQBlockOption(value)
		require.ErrorIs(t, err, ErrBlockInvalidSize)
	}
}

func TestMissingBlocksCodec(t *testing.T) {
	blocks := []uint32{0, 1, 23, 24, 255, 256, 65535, 65536, maxQBlockNumber}
	want := []byte{
		0, 1, 23, 0x18, 24, 0x18, 255, 0x19, 1, 0, 0x19, 255, 255,
		0x1a, 0, 1, 0, 0, 0x1a, 0, 15, 255, 255,
	}
	got, err := EncodeMissingBlocks(nil, blocks)
	require.NoError(t, err)
	require.Equal(t, want, got)
	decoded, err := DecodeMissingBlocks(nil, got)
	require.NoError(t, err)
	require.Equal(t, blocks, decoded)

	t.Run("append", func(t *testing.T) {
		got, err := EncodeMissingBlocks([]byte{0xaa}, []uint32{0, 24})
		require.NoError(t, err)
		require.Equal(t, []byte{0xaa, 0, 0x18, 24}, got)
		values, err := DecodeMissingBlocks([]uint32{42}, []byte{0, 0, 1})
		require.NoError(t, err)
		require.Equal(t, []uint32{42, 0, 1}, values)
	})
	t.Run("nonpreferred-widths", func(t *testing.T) {
		// RFC 8949 well-formed unsigned integers need not use the shortest width.
		values, err := DecodeMissingBlocks(nil, []byte{
			0x18, 1, 0x19, 0, 2, 0x1a, 0, 0, 0, 3,
			0x1b, 0, 0, 0, 0, 0, 0, 0, 4,
		})
		require.NoError(t, err)
		require.Equal(t, []uint32{1, 2, 3, 4}, values)
	})
}

func TestMissingBlocksInvalid(t *testing.T) {
	for _, blocks := range [][]uint32{nil, {}, {1, 1}, {2, 1}, {1 << 20}, {0, 1 << 20}} {
		prefix := []byte{0xff}
		got, err := EncodeMissingBlocks(prefix, blocks)
		require.ErrorIs(t, err, ErrInvalidMissingBlocks)
		require.Equal(t, prefix, got)
	}
	for _, data := range [][]byte{
		nil,
		{},
		{0x80},
		{0x81, 0},
		{0x20},
		{0xf6},
		{0xff},
		{0x1c},
		{0x1d},
		{0x1e},
		{0x1f},
		{0x18},
		{0x19, 1},
		{0x1a, 0, 0, 0},
		{0x1b, 0, 0, 0, 0, 0, 0, 0},
		{0x1a, 0, 0x10, 0, 0},
		{0x1b, 255, 255, 255, 255, 255, 255, 255, 255},
		{2, 1},
		{0, 0, 1, 0},
		{0, 0x80},
	} {
		backing := []uint32{42, 43, 44}
		got, err := DecodeMissingBlocks(backing[:1], data)
		require.ErrorIs(t, err, ErrInvalidMissingBlocks, "%x", data)
		require.Equal(t, []uint32{42}, got)
		require.Equal(t, []uint32{42, 43, 44}, backing, "no partial mutation on error")
	}
}

func TestMissingBlocksNoAllocationsWithCapacity(t *testing.T) {
	blocks := []uint32{0, 1, 23, 24, 255, 256, 65535, 65536, maxQBlockNumber}
	buf := make([]byte, 0, 64)
	values := make([]uint32, 0, len(blocks))
	encoded, err := EncodeMissingBlocks(buf, blocks)
	require.NoError(t, err)
	require.Zero(t, testing.AllocsPerRun(100, func() {
		_, encErr := EncodeMissingBlocks(buf, blocks)
		if encErr != nil {
			panic(encErr)
		}
		_, decErr := DecodeMissingBlocks(values, encoded)
		if decErr != nil {
			panic(decErr)
		}
	}))
}

func FuzzQBlockCodec(f *testing.F) {
	for _, value := range []uint32{0, 6, 7, 0xfffff6, 0xfffffe, 0xffffff, 1 << 24} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value uint32) {
		szx, num, more, err := DecodeQBlockOption(value)
		if err != nil {
			return
		}
		got, err := EncodeQBlockOption(szx, num, more)
		require.NoError(t, err)
		require.Equal(t, value, got)
	})
}

func FuzzMissingBlocks(f *testing.F) {
	for _, data := range [][]byte{{0}, {0, 0, 1}, {0x18, 24}, {0x1a, 0, 15, 255, 255}, {0x80}, {0x1b}} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		values, err := DecodeMissingBlocks(nil, data)
		if err != nil {
			return
		}
		require.NotEmpty(t, values)
		require.LessOrEqual(t, len(values), len(data))
		for i, value := range values {
			require.LessOrEqual(t, value, uint32(maxQBlockNumber))
			if i > 0 {
				require.Greater(t, value, values[i-1])
			}
		}
		encoded, err := EncodeMissingBlocks(nil, values)
		require.NoError(t, err)
		roundtrip, err := DecodeMissingBlocks(nil, encoded)
		require.NoError(t, err)
		require.Equal(t, values, roundtrip)
		canonical, err := EncodeMissingBlocks(nil, roundtrip)
		require.NoError(t, err)
		require.True(t, bytes.Equal(encoded, canonical))
	})
}

func BenchmarkQBlockCodec(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		value, err := EncodeQBlockOption(SZX1024, int64(i&maxQBlockNumber), true)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := DecodeQBlockOption(value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMissingBlocksCodec(b *testing.B) {
	blocks := []uint32{0, 2, 4, 6, 8, 10, 24, 256, 65536, maxQBlockNumber}
	buf := make([]byte, 0, 64)
	values := make([]uint32, 0, len(blocks))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		data, err := EncodeMissingBlocks(buf, blocks)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := DecodeMissingBlocks(values, data); err != nil {
			b.Fatal(err)
		}
	}
}
