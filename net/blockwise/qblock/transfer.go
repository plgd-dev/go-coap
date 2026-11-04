package qblock

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
)

// ValidateQBlockwiseConfig checks mode and blockwise support before a connection
// is opened. Q-Block can be enabled on both UDP and DTLS.
func ValidateQBlockwiseConfig(mode QBlockwiseMode, blockwiseEnabled bool) error {
	if mode > QBlockwiseOnly {
		return ErrQBlockInvalidMode
	}
	if mode == QBlockwiseDisabled {
		return nil
	}
	if !blockwiseEnabled {
		return ErrQBlockNeedsBlockwise
	}
	return nil
}

const (
	qBlockMaxSZX = SZX1024
	qBlockMaxNUM = int64(0xfffff)
)

// QBlockwiseMode selects RFC 9177 use for datagram transfers.
type QBlockwiseMode uint8

const (
	// QBlockwiseDisabled preserves the existing RFC 7959 behavior.
	QBlockwiseDisabled QBlockwiseMode = iota
	// QBlockwiseAuto enables capability discovery before selecting Q-Block.
	QBlockwiseAuto
	// QBlockwiseOnly requires Q-Block and must fail if it cannot be used.
	QBlockwiseOnly
)

var (
	ErrQBlockInvalidSize    = errors.New("invalid Q-Block body size")
	ErrQBlockInvalidNUM     = errors.New("invalid Q-Block number")
	ErrQBlockIncomplete     = errors.New("Q-Block body is incomplete")
	ErrQBlockConflict       = errors.New("conflicting duplicate Q-Block")
	ErrQBlockTooLarge       = errors.New("Q-Block body exceeds configured limit")
	ErrQBlockScheduleFull   = errors.New("Q-Block deadline scheduler is full")
	ErrQBlockInvalidMode    = errors.New("invalid Q-Block mode")
	ErrQBlockNeedsBlockwise = errors.New("Q-Block requires blockwise to be enabled")
	ErrQBlockDisabled       = errors.New("Q-Block is disabled by policy")
	ErrQBlockUnsupported    = errors.New("peer does not support Q-Block")
)

// Assembly stores out-of-order datagram blocks without allocating the
// advertised body size up front. The caller must enforce aggregate limits
// across assemblies; maxBodySize and maxBlocks bound each individual assembly.
type Assembly struct {
	mu           sync.Mutex
	szx          SZX
	bodySize     int64
	maxBodySize  int64
	maxBlocks    int
	blocks       map[int64][]byte
	finalBlock   int64
	receivedSize int64
	firstMissing int64
}

// NewAssembly creates a sparse body receiver. A zero maxBodySize or
// maxBlocks is invalid: Q-Block input must always have explicit resource bounds.
func NewAssembly(szx SZX, bodySize, maxBodySize int64, maxBlocks int) (*Assembly, error) {
	if szx > qBlockMaxSZX || bodySize < 0 {
		return nil, ErrQBlockInvalidSize
	}
	if maxBodySize <= 0 || bodySize > maxBodySize {
		return nil, ErrQBlockTooLarge
	}
	blockSize := szx.Size()
	if blockSize <= 0 {
		return nil, ErrInvalidSZX
	}
	blockCount := int64(0)
	if bodySize > 0 {
		blockCount = (bodySize-1)/blockSize + 1
	}
	if maxBlocks <= 0 || blockCount > int64(maxBlocks) || blockCount-1 > qBlockMaxNUM {
		return nil, ErrQBlockTooLarge
	}
	return &Assembly{
		szx:         szx,
		bodySize:    bodySize,
		maxBodySize: maxBodySize,
		maxBlocks:   maxBlocks,
		blocks:      make(map[int64][]byte),
		finalBlock:  -1,
	}, nil
}

// Add stores a payload at NUM. Duplicate payloads are ignored, including
// conflicting bytes: the first accepted payload remains authoritative.
func (a *Assembly) Add(num int64, more bool, payload []byte) (complete, duplicate bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if num < 0 || num > qBlockMaxNUM {
		return false, false, ErrQBlockInvalidNUM
	}
	if _, ok := a.blocks[num]; ok {
		return a.completeLocked(), true, nil
	}
	if err := a.validatePayloadLocked(num, more, payload); err != nil {
		return false, false, err
	}
	if a.finalBlock >= 0 && num > a.finalBlock || !more && a.finalBlock >= 0 && a.finalBlock != num {
		return false, false, ErrQBlockInvalidSize
	}
	if !more {
		for receivedNum := range a.blocks {
			if receivedNum > num {
				return false, false, ErrQBlockInvalidSize
			}
		}
		a.finalBlock = num
	}
	a.blocks[num] = bytes.Clone(payload)
	a.receivedSize += int64(len(payload))
	for {
		if _, ok := a.blocks[a.firstMissing]; !ok {
			break
		}
		a.firstMissing++
	}
	return a.completeLocked(), false, nil
}

func (a *Assembly) validatePayloadLocked(num int64, more bool, payload []byte) error {
	blockSize := a.szx.Size()
	lastNUM := (max(a.bodySize, 1) - 1) / blockSize
	if num > lastNUM {
		return ErrQBlockInvalidNUM
	}
	offset := num * blockSize
	end := offset + int64(len(payload))
	if end < offset || end > a.bodySize || int64(len(payload)) > blockSize {
		return ErrQBlockInvalidSize
	}
	if more {
		if len(payload) != int(blockSize) || end >= a.bodySize {
			return ErrQBlockInvalidSize
		}
	} else if end != a.bodySize {
		return ErrQBlockInvalidSize
	}
	if len(a.blocks) >= a.maxBlocks {
		return ErrQBlockTooLarge
	}
	return nil
}

// AddOption decodes a Q-Block option and adds its payload to the assembly.
func (a *Assembly) AddOption(value uint32, payload []byte) (complete, duplicate bool, err error) {
	szx, num, more, err := DecodeQBlockOption(value)
	if err != nil {
		return false, false, err
	}
	if szx != a.szx {
		return false, false, ErrQBlockInvalidSize
	}
	return a.Add(num, more, payload)
}

func (a *Assembly) completeLocked() bool {
	if a.bodySize == 0 {
		return a.finalBlock == 0 && len(a.blocks) == 1
	}
	if a.finalBlock < 0 || len(a.blocks) != int(a.finalBlock+1) {
		return false
	}
	return a.receivedSize == a.bodySize
}

// Complete reports whether all bytes of the declared body have been received.
func (a *Assembly) Complete() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.completeLocked()
}

// Missing returns the ascending NUM sequence still needed to complete the
// declared body. The returned slice is independent of the assembly.
func (a *Assembly) Missing() []uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	blockCount := int64(1)
	if a.bodySize > 0 {
		blockCount = (a.bodySize-1)/a.szx.Size() + 1
	}
	if blockCount > int64(a.maxBlocks) {
		return nil
	}
	missing := make([]uint32, 0, int(blockCount)-len(a.blocks))
	for num := int64(0); num < blockCount; num++ {
		if _, ok := a.blocks[num]; !ok {
			missing = append(missing, uint32(num)) // #nosec G115 -- constructor limits block count to the 20-bit NUM range.
		}
	}
	return missing
}

// FirstMissing reports the first gap in constant time. The cursor advances
// monotonically, avoiding a full-body scan for each arriving payload.
func (a *Assembly) FirstMissing() (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := max(int64(1), (a.bodySize+a.szx.Size()-1)/a.szx.Size())
	return a.firstMissing, a.firstMissing < count
}

// MissingSet returns the gaps in the earliest incomplete payload set only.
func (a *Assembly) MissingSet() []uint32 {
	return a.missingSet(DefaultConfig().MaxPayloads)
}

func (a *Assembly) missingSet(maxPayloads int64) []uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	count := max(int64(1), (a.bodySize+a.szx.Size()-1)/a.szx.Size())
	var missing []uint32
	for num, last := a.firstMissing, (Config{MaxPayloads: maxPayloads}).setLast(a.firstMissing, count); num <= last; num++ {
		if _, ok := a.blocks[num]; !ok {
			missing = append(missing, uint32(num)) // #nosec G115 -- construction validates the 20-bit block count.
		}
	}
	return missing
}

// ReadSeeker returns a complete body reader over the immutable sparse chunks,
// without allocating a second copy of the representation.
func (a *Assembly) ReadSeeker() (io.ReadSeeker, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.completeLocked() {
		return nil, ErrQBlockIncomplete
	}
	return &qSparseReader{blocks: a.blocks, size: a.bodySize, blockSize: a.szx.Size()}, nil
}

type qSparseReader struct {
	blocks                  map[int64][]byte
	size, blockSize, offset int64
}

func (r *qSparseReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset >= r.size {
		return 0, io.EOF
	}
	n := 0
	for len(p) != 0 && r.offset < r.size {
		block := r.blocks[r.offset/r.blockSize]
		copied := copy(p, block[r.offset%r.blockSize:])
		p, n, r.offset = p[copied:], n+copied, r.offset+int64(copied)
	}
	return n, nil
}

func (r *qSparseReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.offset
	case io.SeekEnd:
		base = r.size
	default:
		return r.offset, errors.New("invalid seek whence")
	}
	if offset < -base || offset > math.MaxInt64-base {
		return r.offset, errors.New("invalid seek position")
	}
	r.offset = base + offset
	return r.offset, nil
}

// Sender provides bounded random access to a replayable body. It does not
// copy the representation or retain per-block payloads.
type Sender struct {
	mu       sync.Mutex
	body     io.ReadSeeker
	szx      SZX
	bodySize int64
}

func NewSender(body io.ReadSeeker, szx SZX, bodySize int64) (*Sender, error) {
	if body == nil || szx > qBlockMaxSZX || bodySize < 0 {
		return nil, ErrQBlockInvalidSize
	}
	if szx.Size() <= 0 {
		return nil, ErrInvalidSZX
	}
	blocks := int64(0)
	if bodySize > 0 {
		blocks = (bodySize-1)/szx.Size() + 1
	}
	if blocks > qBlockMaxNUM+1 {
		return nil, ErrQBlockInvalidSize
	}
	return &Sender{body: body, szx: szx, bodySize: bodySize}, nil
}

// Block reads one body block. NUM values and payload boundaries are validated
// against the declared body size.
func (s *Sender) Block(num int64) (payload []byte, more bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if num < 0 || num > qBlockMaxNUM {
		return nil, false, ErrQBlockInvalidNUM
	}
	blockSize := s.szx.Size()
	offset := num * blockSize
	if offset > s.bodySize || offset == s.bodySize && s.bodySize != 0 {
		return nil, false, ErrQBlockInvalidNUM
	}
	length := blockSize
	if remaining := s.bodySize - offset; remaining < length {
		length = remaining
	}
	position, err := s.body.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, false, fmt.Errorf("seek Q-Block body: %w", err)
	}
	if position != offset {
		return nil, false, fmt.Errorf("seek Q-Block body: got offset %d, want %d", position, offset)
	}
	payload = make([]byte, length)
	n, readErr := io.ReadFull(s.body, payload)
	if readErr != nil && (readErr != io.EOF || length != 0) && readErr != io.ErrUnexpectedEOF {
		return nil, false, fmt.Errorf("read Q-Block body: %w", readErr)
	}
	if int64(n) != length {
		return nil, false, io.ErrUnexpectedEOF
	}
	payload = payload[:n]
	more = offset+int64(n) < s.bodySize
	return payload, more, nil
}

// BlockOption encodes the Q-Block option value corresponding to Block(num).
func (s *Sender) BlockOption(num int64) (uint32, error) {
	if num < 0 || num > qBlockMaxNUM {
		return 0, ErrQBlockInvalidNUM
	}
	offset := num * s.szx.Size()
	if offset > s.bodySize || offset == s.bodySize && s.bodySize != 0 {
		return 0, ErrQBlockInvalidNUM
	}
	more := offset+s.szx.Size() < s.bodySize
	return EncodeQBlockOption(s.szx, num, more)
}

// Blocks reads a sorted, deduplicated NUM list. maxPayloads bounds the amount
// of payload memory returned to the caller.
func (s *Sender) Blocks(nums []uint32, maxPayloads int) ([][]byte, error) {
	if maxPayloads <= 0 {
		return nil, ErrQBlockTooLarge
	}
	ordered := append([]uint32(nil), nums...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	unique := ordered[:0]
	for _, num := range ordered {
		if len(unique) == 0 || unique[len(unique)-1] != num {
			unique = append(unique, num)
		}
	}
	if len(unique) > maxPayloads {
		return nil, ErrQBlockTooLarge
	}
	out := make([][]byte, 0, len(unique))
	for _, num := range unique {
		payload, _, err := s.Block(int64(num))
		if err != nil {
			return nil, err
		}
		out = append(out, payload)
	}
	return out, nil
}
