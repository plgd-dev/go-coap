package qblock

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"

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
	meta     Metadata
	pages    []*bodyPage
	count    uint32
	received uint32
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
	identity := make([]byte, len(meta.Identity))
	copy(identity, meta.Identity)
	meta.Identity = identity
	return &Body{meta: meta, pages: make([]*bodyPage, (count+63)/64), count: uint32(count)}, nil
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
	if b.has(block.Number) {
		return true, nil
	}
	index := block.Number / 64
	page := b.pages[index]
	if page == nil {
		start := uint64(index) * 64 * blockSize
		length := min(64*blockSize, uint64(b.meta.Size)-start)
		page = &bodyPage{payload: make([]byte, int(length))}
		b.pages[index] = page
	}
	copy(page.payload[uint64(block.Number%64)*blockSize:], payload)
	page.present |= uint64(1) << (block.Number % 64)
	b.received++
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
		if !b.has(n) {
			missing = append(missing, n)
		}
	}
	return missing
}

// Complete reports whether all blocks, including the final block, were received.
func (b *Body) Complete() bool { return b.received == b.count }

// Assemble returns a new contiguous copy; the caller must budget this allocation
// in addition to the sparse storage. It fails if the body is incomplete.
func (b *Body) Assemble() ([]byte, error) {
	if !b.Complete() {
		return nil, errors.New("body is incomplete")
	}
	result := make([]byte, int(b.meta.Size))
	size := uint64(16) << b.meta.SZX
	for index, page := range b.pages {
		copy(result[uint64(index)*64*size:], page.payload)
	}
	return result, nil
}

// Each allocated page owns a bounded contiguous range and a presence bitmap.
// Directory allocation is bounded by the legal twenty-bit block number range.
type bodyPage struct {
	payload []byte
	present uint64
}

func (b *Body) has(number uint32) bool {
	if number >= b.count {
		return false
	}
	page := b.pages[number/64]
	return page != nil && page.present&(uint64(1)<<(number%64)) != 0
}

// bodyStorageBytes includes all body payload, assembly and application indexing
// that can coexist. It excludes allocator/runtime overhead by design.
func bodyStorageBytes(meta Metadata) (uint64, error) {
	if meta.SZX > blockwise.SZX1024 {
		return 0, blockwise.ErrInvalidSZX
	}
	count := max(uint64(1), (uint64(meta.Size)+(uint64(16)<<meta.SZX)-1)/(uint64(16)<<meta.SZX))
	if count > 1<<20 {
		return 0, errors.New("body exceeds block number limit")
	}
	pages := (count + 63) / 64
	return 2*uint64(meta.Size) + pages*(uint64(unsafe.Sizeof((*bodyPage)(nil)))+uint64(unsafe.Sizeof(bodyPage{}))) + uint64(unsafe.Sizeof(Body{})) + uint64(len(meta.Identity)), nil
}
