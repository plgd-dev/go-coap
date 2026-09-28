package qblock

import (
	"bytes"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
)

// Metadata describes one fixed-size representation being assembled.
type Metadata struct {
	Size             uint32
	SZX              blockwise.SZX
	ContentFormat    message.MediaType
	HasContentFormat bool
	Identity         []byte
}

// Body owns sparse blocks until the exact body is complete.
type Body struct {
	meta     Metadata
	blocks   map[uint32][]byte
	count    uint32
	received uint32
}

func NewBody(meta Metadata, maxBytes uint32) (*Body, error) {
	if meta.Size > maxBytes {
		return nil, fmt.Errorf("body size %d exceeds limit %d", meta.Size, maxBytes)
	}
	if meta.SZX > blockwise.SZX1024 {
		return nil, fmt.Errorf("unsupported body SZX %d", meta.SZX)
	}
	blockSize := uint64(meta.SZX.Size())
	count := (uint64(meta.Size) + blockSize - 1) / blockSize
	if count == 0 {
		count = 1
	}
	if count > maxBlockNumber+1 {
		return nil, fmt.Errorf("body requires %d blocks", count)
	}
	if uint64(meta.Size) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("body size exceeds addressable memory")
	}
	meta.Identity = bytes.Clone(meta.Identity)
	return &Body{meta: meta, count: uint32(count)}, nil
}

func (b *Body) Add(meta Metadata, block Block, payload []byte) (bool, error) {
	if meta.Size != b.meta.Size || meta.SZX != b.meta.SZX ||
		meta.HasContentFormat != b.meta.HasContentFormat ||
		(meta.HasContentFormat && meta.ContentFormat != b.meta.ContentFormat) ||
		!bytes.Equal(meta.Identity, b.meta.Identity) {
		return false, fmt.Errorf("body metadata changed")
	}
	if block.SZX != b.meta.SZX || block.Number >= b.count {
		return false, fmt.Errorf("block is outside body or has wrong SZX")
	}
	blockSize := uint64(b.meta.SZX.Size())
	offset := uint64(block.Number) * blockSize
	remaining := uint64(b.meta.Size) - offset
	expected := blockSize
	if remaining < expected {
		expected = remaining
	}
	if uint64(len(payload)) != expected || block.More != (block.Number+1 < b.count) {
		return false, fmt.Errorf("block length or More flag does not match body size")
	}
	if _, ok := b.blocks[block.Number]; ok {
		return true, nil
	}
	if b.blocks == nil {
		b.blocks = make(map[uint32][]byte)
	}
	b.blocks[block.Number] = bytes.Clone(payload)
	b.received++
	return false, nil
}

// Missing reports absent indices in the half-open interval [first, end).
func (b *Body) Missing(first, end uint32, limit int) []uint32 {
	if limit <= 0 || first >= end || first >= b.count {
		return nil
	}
	if end > b.count {
		end = b.count
	}
	var missing []uint32
	for number := first; number < end && len(missing) < limit; number++ {
		if _, ok := b.blocks[number]; !ok {
			missing = append(missing, number)
		}
	}
	return missing
}

func (b *Body) Complete() bool {
	return b.received == b.count
}

// Assemble returns a new contiguous copy after all blocks arrive.
func (b *Body) Assemble() ([]byte, error) {
	if !b.Complete() {
		return nil, fmt.Errorf("body is incomplete")
	}
	assembled := make([]byte, int(b.meta.Size))
	blockSize := uint64(b.meta.SZX.Size())
	for number := uint32(0); number < b.count; number++ {
		copy(assembled[int(uint64(number)*blockSize):], b.blocks[number])
	}
	return assembled, nil
}
