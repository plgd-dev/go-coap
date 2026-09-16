package qblock

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
)

// Metadata identifies a body's immutable representation parameters.
// Identity is an engine-supplied operation/representation key.
type Metadata struct {
	Size             uint32
	SZX              blockwise.SZX
	ContentFormat    message.MediaType
	HasContentFormat bool
	Identity         []byte
}

// Body owns sparse copied payloads. Access must be serialized by its caller.
// maxBytes bounds payload size; aggregate memory accounting belongs to the engine.
type Body struct {
	meta   Metadata
	blocks map[uint32][]byte
	count  uint32
}

// NewBody validates the announced size without allocating the announced body.
func NewBody(meta Metadata, maxBytes uint32) (*Body, error) {
	if maxBytes == 0 || meta.Size > maxBytes {
		return nil, errors.New("body exceeds byte limit")
	}
	if meta.SZX > blockwise.SZX1024 {
		return nil, blockwise.ErrInvalidSZX
	}
	size := uint64(16) << meta.SZX
	count := (uint64(meta.Size) + size - 1) / size
	if count == 0 {
		count = 1
	}
	if count > 1<<20 {
		return nil, errors.New("body exceeds block number limit")
	}
	meta.Identity = bytes.Clone(meta.Identity)
	return &Body{meta: meta, blocks: make(map[uint32][]byte), count: uint32(count)}, nil
}

// Add copies a validated fragment. A duplicate never overwrites existing data.
func (b *Body) Add(meta Metadata, block Block, payload []byte) (bool, error) {
	if meta.Size != b.meta.Size || meta.SZX != b.meta.SZX || meta.HasContentFormat != b.meta.HasContentFormat ||
		(meta.HasContentFormat && meta.ContentFormat != b.meta.ContentFormat) || !bytes.Equal(meta.Identity, b.meta.Identity) {
		return false, errors.New("body metadata changed")
	}
	if block.SZX != b.meta.SZX || block.Number >= b.count {
		return false, errors.New("invalid body block")
	}
	blockSize := uint64(16) << block.SZX
	offset := uint64(block.Number) * blockSize
	expected := min(blockSize, uint64(b.meta.Size)-offset)
	if uint64(len(payload)) != expected || block.More != (block.Number+1 < b.count) {
		return false, fmt.Errorf("block %d has inconsistent length or more bit", block.Number)
	}
	if _, ok := b.blocks[block.Number]; ok {
		return true, nil
	}
	b.blocks[block.Number] = bytes.Clone(payload)
	return false, nil
}

// Missing returns up to limit absent block numbers in [first, end).
func (b *Body) Missing(first, end uint32, limit int) []uint32 {
	var missing []uint32
	if limit <= 0 {
		return missing
	}
	end = min(end, b.count)
	for n := first; n < end && len(missing) < limit; n++ {
		if _, ok := b.blocks[n]; !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// Complete reports whether all blocks, including the final block, were received.
func (b *Body) Complete() bool { return uint64(len(b.blocks)) == uint64(b.count) }

// Assemble returns a new contiguous copy; the caller must budget this allocation
// in addition to the sparse storage. It fails if the body is incomplete.
func (b *Body) Assemble() ([]byte, error) {
	if !b.Complete() {
		return nil, errors.New("body is incomplete")
	}
	result := make([]byte, int(b.meta.Size))
	size := uint64(16) << b.meta.SZX
	for n, payload := range b.blocks {
		copy(result[uint64(n)*size:], payload)
	}
	return result, nil
}
