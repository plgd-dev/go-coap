package qblock

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestBlockRoundTrip(t *testing.T) {
	for _, number := range []uint32{0, 1, 1048575} {
		for szx := blockwise.SZX16; szx <= blockwise.SZX1024; szx++ {
			for _, more := range []bool{false, true} {
				want := Block{Number: number, More: more, SZX: szx}
				value, err := EncodeBlock(want)
				require.NoError(t, err)
				got, err := DecodeBlock(value)
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
		}
	}
	value, err := EncodeBlock(Block{Number: 1048575, More: true, SZX: blockwise.SZX1024})
	require.NoError(t, err)
	require.Equal(t, uint32(0xfffffe), value)
}

func TestBlockRejectsOutOfRangeValues(t *testing.T) {
	_, err := EncodeBlock(Block{Number: 1048576, SZX: blockwise.SZX16})
	require.Error(t, err)
	_, err = EncodeBlock(Block{SZX: blockwise.SZXBERT})
	require.Error(t, err)
	_, err = DecodeBlock(7)
	require.Error(t, err)
	_, err = DecodeBlock(0xffffff)
	require.Error(t, err)
	_, err = DecodeBlock(0x1000000)
	require.Error(t, err)
}
