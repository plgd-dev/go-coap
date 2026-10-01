package qblock

import (
	"bytes"
	"testing"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestBodyFinalBlockFirst(t *testing.T) {
	meta := Metadata{Size: 17, SZX: blockwise.SZX16, Identity: []byte("body")}
	b, err := NewBody(meta, 64)
	require.NoError(t, err)
	_, err = b.Assemble()
	require.Error(t, err)
	duplicate, err := b.Add(meta, Block{Number: 1, SZX: blockwise.SZX16}, []byte{9})
	require.NoError(t, err)
	require.False(t, duplicate)
	require.False(t, b.Complete())
	require.Equal(t, []uint32{0}, b.Missing(0, 2, 10))
	first := make([]byte, 16)
	_, err = b.Add(meta, Block{More: true, SZX: blockwise.SZX16}, first)
	require.NoError(t, err)
	first[0] = 99
	require.True(t, b.Complete())
	duplicate, err = b.Add(meta, Block{More: true, SZX: blockwise.SZX16}, first)
	require.NoError(t, err)
	require.True(t, duplicate)
	got, err := b.Assemble()
	require.NoError(t, err)
	require.Len(t, got, 17)
	require.Zero(t, got[0])
	require.Equal(t, byte(9), got[16])
	got[0] = 42
	again, err := b.Assemble()
	require.NoError(t, err)
	require.Zero(t, again[0])
}

func TestBodyValidation(t *testing.T) {
	meta := Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte{1}, HasContentFormat: true}
	for _, tc := range []struct {
		name   string
		change func(*Metadata, *Block, *[]byte)
	}{
		{"identity", func(m *Metadata, _ *Block, _ *[]byte) { m.Identity = []byte{2} }},
		{"size", func(m *Metadata, _ *Block, _ *[]byte) { m.Size = 33 }},
		{"szx", func(m *Metadata, _ *Block, _ *[]byte) { m.SZX = blockwise.SZX32 }},
		{"format", func(m *Metadata, _ *Block, _ *[]byte) { m.ContentFormat = 1 }},
		{"presence", func(m *Metadata, _ *Block, _ *[]byte) { m.HasContentFormat = false }},
		{"block szx", func(_ *Metadata, b *Block, _ *[]byte) { b.SZX = blockwise.SZX32 }},
		{"number", func(_ *Metadata, b *Block, _ *[]byte) { b.Number = 2 }},
		{"huge number", func(_ *Metadata, b *Block, _ *[]byte) { b.Number = ^uint32(0) }},
		{"more", func(_ *Metadata, b *Block, _ *[]byte) { b.More = false }},
		{"short", func(_ *Metadata, _ *Block, p *[]byte) { *p = make([]byte, 15) }},
		{"long", func(_ *Metadata, _ *Block, p *[]byte) { *p = make([]byte, 17) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewBody(meta, 64)
			require.NoError(t, err)
			block := Block{More: true, SZX: blockwise.SZX16}
			payload := make([]byte, 16)
			_, err = b.Add(meta, block, payload)
			require.NoError(t, err)
			changed := meta
			tc.change(&changed, &block, &payload)
			_, err = b.Add(changed, block, payload)
			require.Error(t, err)
			require.False(t, b.Complete())
			require.Equal(t, []uint32{1}, b.Missing(0, 3, 4))
		})
	}
}

func TestBodyLimitsAndEmpty(t *testing.T) {
	for _, m := range []Metadata{{Size: 65}, {Size: 1, SZX: blockwise.SZXBERT}, {Size: ^uint32(0)}} {
		_, err := NewBody(m, 64)
		require.Error(t, err)
	}
	_, err := NewBody(Metadata{Size: 16777217}, ^uint32(0))
	require.Error(t, err)
	_, err = NewBody(Metadata{}, 0)
	require.Error(t, err)
	b, err := NewBody(Metadata{}, 1)
	require.NoError(t, err)
	require.False(t, b.Complete())
	_, err = b.Add(Metadata{}, Block{}, nil)
	require.NoError(t, err)
	require.True(t, b.Complete())
	got, err := b.Assemble()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestBodyRangesAndIdentityOwnership(t *testing.T) {
	meta := Metadata{Size: 64, Identity: []byte{1}}
	b, err := NewBody(meta, 64)
	require.NoError(t, err)
	meta.Identity[0] = 2
	_, err = b.Add(meta, Block{More: true}, make([]byte, 16))
	require.Error(t, err)
	meta.Identity = []byte{1}
	_, err = b.Add(meta, Block{Number: 3}, make([]byte, 16))
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, b.Missing(0, ^uint32(0), 2))
	require.Equal(t, []uint32{1, 2}, b.Missing(1, 4, 100))
	require.Empty(t, b.Missing(4, 1, 100))
	require.Empty(t, b.Missing(0, 4, 0))
	require.Empty(t, b.Missing(0, 4, -1))
	for n := uint32(0); n < 3; n++ {
		_, err = b.Add(meta, Block{Number: n, More: true}, bytes.Repeat([]byte{byte(n)}, 16))
		require.NoError(t, err)
	}
	require.True(t, b.Complete())
	require.Empty(t, b.Missing(0, 4, 4))
}

func TestBodySparseStorage(t *testing.T) {
	meta := Metadata{Size: 1 << 30, SZX: blockwise.SZX1024}
	b, err := NewBody(meta, 1<<30)
	require.NoError(t, err)
	_, err = b.Add(meta, Block{Number: (1 << 20) - 1, SZX: blockwise.SZX1024}, make([]byte, 1024))
	require.NoError(t, err)
	// Inspect retained payloads to prove a final fragment did not allocate Size bytes.
	require.EqualValues(t, 1, b.received)
	require.Len(t, b.pages, 1<<14)
	require.Len(t, b.pages[len(b.pages)-1].payload, 64*1024)
	require.Nil(t, b.pages[0])
}

func TestBodyPagedStorageDuplicateAndCrossPageAssembly(t *testing.T) {
	meta := Metadata{Size: 65*16 - 3, SZX: blockwise.SZX16}
	body, err := NewBody(meta, meta.Size)
	require.NoError(t, err)
	for number := uint32(64); ; number-- {
		length := 16
		if number == 64 {
			length = 13
		}
		payload := bytes.Repeat([]byte{byte(number)}, length)
		duplicate, err := body.Add(meta, Block{Number: number, More: number < 64, SZX: meta.SZX}, payload)
		require.NoError(t, err)
		require.False(t, duplicate)
		payload[0] = 255
		duplicate, err = body.Add(meta, Block{Number: number, More: number < 64, SZX: meta.SZX}, payload)
		require.NoError(t, err)
		require.True(t, duplicate)
		if number == 0 {
			break
		}
	}
	require.True(t, body.Complete())
	assembled, err := body.Assemble()
	require.NoError(t, err)
	require.Len(t, assembled, int(meta.Size))
	for index, value := range assembled {
		require.Equal(t, byte(index/16), value)
	}
	require.Empty(t, body.Missing(0, 65, 65))
}

func TestBodyIdentityRetainsExactBacking(t *testing.T) {
	identity := make([]byte, 6, 128)
	copy(identity, []byte("etag-a"))
	body, err := NewBody(Metadata{Size: 16, SZX: blockwise.SZX16, Identity: identity}, 16)
	require.NoError(t, err)
	require.Equal(t, len(body.meta.Identity), cap(body.meta.Identity), "identity charge includes exact detached backing")
	identity[0] = 'x'
	require.Equal(t, []byte("etag-a"), body.meta.Identity)
}
