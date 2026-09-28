package qblock

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestBodyFinalBlockFirst(t *testing.T) {
	meta := Metadata{Size: 17, SZX: blockwise.SZX16, Identity: []byte("body")}
	b, err := NewBody(meta, 64)
	require.NoError(t, err)
	_, err = b.Add(meta, Block{Number: 1, SZX: blockwise.SZX16}, []byte{9})
	require.NoError(t, err)
	require.False(t, b.Complete())
	require.Equal(t, []uint32{0}, b.Missing(0, 2, 10))
	first := make([]byte, 16)
	_, err = b.Add(meta, Block{Number: 0, More: true, SZX: blockwise.SZX16}, first)
	require.NoError(t, err)
	first[0] = 99
	require.True(t, b.Complete())
	got, err := b.Assemble()
	require.NoError(t, err)
	require.Len(t, got, 17)
	require.Zero(t, got[0])
	require.Equal(t, byte(9), got[16])
	got[16] = 88
	again, err := b.Assemble()
	require.NoError(t, err)
	require.Equal(t, byte(9), again[16])
}

func TestBodyDuplicateAndMetadata(t *testing.T) {
	meta := Metadata{Size: 2, SZX: blockwise.SZX16, ContentFormat: message.AppOctets, HasContentFormat: true, Identity: []byte("key")}
	b, err := NewBody(meta, 2)
	require.NoError(t, err)
	meta.Identity[0] = 'X'
	addMeta := Metadata{Size: 2, SZX: blockwise.SZX16, ContentFormat: message.AppOctets, HasContentFormat: true, Identity: []byte("key")}
	payload := []byte{1, 2}
	duplicate, err := b.Add(addMeta, Block{SZX: blockwise.SZX16}, payload)
	require.NoError(t, err)
	require.False(t, duplicate)
	payload[0] = 9
	duplicate, err = b.Add(addMeta, Block{SZX: blockwise.SZX16}, []byte{8, 8})
	require.NoError(t, err)
	require.True(t, duplicate)
	got, err := b.Assemble()
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2}, got)
	for _, changed := range []Metadata{
		{Size: 3, SZX: blockwise.SZX16, ContentFormat: message.AppOctets, HasContentFormat: true, Identity: []byte("key")},
		{Size: 2, SZX: blockwise.SZX32, ContentFormat: message.AppOctets, HasContentFormat: true, Identity: []byte("key")},
		{Size: 2, SZX: blockwise.SZX16, ContentFormat: message.AppJSON, HasContentFormat: true, Identity: []byte("key")},
		{Size: 2, SZX: blockwise.SZX16, ContentFormat: message.AppOctets, Identity: []byte("key")},
		{Size: 2, SZX: blockwise.SZX16, ContentFormat: message.AppOctets, HasContentFormat: true, Identity: []byte("other")},
	} {
		_, err = b.Add(changed, Block{SZX: blockwise.SZX16}, []byte{1, 2})
		require.Error(t, err)
	}
}

func TestBodyRejectsMalformedBlocks(t *testing.T) {
	meta := Metadata{Size: 32, SZX: blockwise.SZX16}
	b, err := NewBody(meta, 32)
	require.NoError(t, err)
	for name, input := range map[string]struct {
		block   Block
		payload []byte
	}{
		"short nonfinal": {Block{Number: 0, More: true, SZX: blockwise.SZX16}, make([]byte, 15)},
		"long final":     {Block{Number: 1, SZX: blockwise.SZX16}, make([]byte, 17)},
		"wrong M":        {Block{Number: 0, SZX: blockwise.SZX16}, make([]byte, 16)},
		"wrong SZX":      {Block{Number: 0, More: true, SZX: blockwise.SZX32}, make([]byte, 16)},
		"past body":      {Block{Number: 2, SZX: blockwise.SZX16}, nil},
		"huge number":    {Block{Number: 1048576, SZX: blockwise.SZX16}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, addErr := b.Add(meta, input.block, input.payload)
			require.Error(t, addErr)
		})
	}
	require.False(t, b.Complete())
	_, err = b.Assemble()
	require.Error(t, err)
}

func TestBodyExactMultipleAndEmpty(t *testing.T) {
	full := Metadata{Size: 16, SZX: blockwise.SZX16}
	b, err := NewBody(full, 16)
	require.NoError(t, err)
	_, err = b.Add(full, Block{SZX: blockwise.SZX16}, make([]byte, 16))
	require.NoError(t, err)
	require.True(t, b.Complete())

	empty := Metadata{SZX: blockwise.SZX16}
	b, err = NewBody(empty, 0)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, b.Missing(0, 2, 1))
	_, err = b.Add(empty, Block{SZX: blockwise.SZX16}, nil)
	require.NoError(t, err)
	require.True(t, b.Complete())
	got, err := b.Assemble()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestBodyLimitsAndMissingRanges(t *testing.T) {
	_, err := NewBody(Metadata{Size: 33, SZX: blockwise.SZX16}, 32)
	require.Error(t, err)
	_, err = NewBody(Metadata{Size: 16*1048576 + 1, SZX: blockwise.SZX16}, 16*1048576+1)
	require.Error(t, err)
	_, err = NewBody(Metadata{Size: 1, SZX: blockwise.SZXBERT}, 1)
	require.Error(t, err)
	meta := Metadata{Size: 49, SZX: blockwise.SZX16}
	b, err := NewBody(meta, 49)
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, b.Missing(0, 4, 2))
	require.Empty(t, b.Missing(0, 4, 0))
	require.Empty(t, b.Missing(4, 9, 3))
	require.Empty(t, b.Missing(3, 2, 3))
	_, err = b.Add(meta, Block{Number: 3, SZX: blockwise.SZX16}, []byte{1})
	require.NoError(t, err)
	require.Equal(t, []uint32{1, 2}, b.Missing(1, 99, 2))
}
