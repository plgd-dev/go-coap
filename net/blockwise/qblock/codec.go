package qblock

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const maxQBlockNumber = 1<<20 - 1

// ErrInvalidMissingBlocks indicates an invalid RFC 9177 missing-block sequence.
var ErrInvalidMissingBlocks = errors.New("invalid missing-block sequence")

// EncodeQBlockOption encodes a datagram Q-Block option (RFC 9177 Section 4.2).
// BERT is not supported on datagram transports.
func EncodeQBlockOption(szx SZX, num int64, more bool) (uint32, error) {
	if szx > SZX1024 {
		return 0, ErrInvalidSZX
	}
	if num < 0 || num > maxQBlockNumber {
		return 0, ErrBlockNumberExceedLimit
	}
	value := uint32(num)<<4 | uint32(szx)
	if more {
		value |= moreBlocksFollowingMask
	}
	return value, nil
}

// DecodeQBlockOption decodes a datagram Q-Block option, including its full
// twenty-bit block number.
func DecodeQBlockOption(value uint32) (szx SZX, num int64, more bool, err error) {
	if value > maxBlockValue {
		return 0, 0, false, ErrBlockInvalidSize
	}
	szx = SZX(value & szxMask)
	if szx > SZX1024 {
		return 0, 0, false, ErrInvalidSZX
	}
	return szx, int64(value >> 4), value&moreBlocksFollowingMask != 0, nil
}

// EncodeMissingBlocks appends an RFC 9177 Section 5 CBOR sequence to dst.
// The list must be nonempty and strictly increasing. No CBOR array wrapper
// is emitted. On error, dst is unchanged.
func EncodeMissingBlocks(dst []byte, blocks []uint32) ([]byte, error) {
	if len(blocks) == 0 {
		return dst, fmt.Errorf("%w: empty list", ErrInvalidMissingBlocks)
	}
	var previous uint32
	for i, block := range blocks {
		if block > maxQBlockNumber || (i > 0 && block <= previous) {
			return dst, fmt.Errorf("%w: block number or order", ErrInvalidMissingBlocks)
		}
		previous = block
	}
	for _, block := range blocks {
		switch {
		case block < 24:
			dst = append(dst, byte(block))
		case block <= 0xff:
			dst = append(dst, 0x18, byte(block))
		case block <= 0xffff:
			dst = append(dst, 0x19, byte(block>>8), byte(block&0xff))
		default:
			dst = append(dst, 0x1a, byte(block>>24), byte((block>>16)&0xff), byte((block>>8)&0xff), byte(block&0xff))
		}
	}
	return dst, nil
}

// DecodeMissingBlocks appends the block numbers in a CBOR sequence to dst.
// Duplicate entries are ignored as required by RFC 9177; descending values,
// non-integers and numbers outside the twenty-bit NUM range are rejected.
// On error, the original dst is returned.
func DecodeMissingBlocks(dst []uint32, data []byte) ([]uint32, error) {
	if len(data) == 0 {
		return dst, fmt.Errorf("%w: empty sequence", ErrInvalidMissingBlocks)
	}
	// Validate first so malformed input does not cause allocations or modify dst.
	if err := walkMissingBlocks(data, nil); err != nil {
		return dst, err
	}
	err := walkMissingBlocks(data, func(block uint32) {
		dst = append(dst, block)
	})
	return dst, err
}

func walkMissingBlocks(data []byte, emit func(uint32)) error {
	var previous uint32
	first := true
	for len(data) > 0 {
		number, rest, err := decodeMissingBlockInteger(data)
		if err != nil {
			return err
		}
		data = rest
		if number > maxQBlockNumber {
			return fmt.Errorf("%w: block number exceeds NUM range", ErrInvalidMissingBlocks)
		}
		block := uint32(number)
		if !first && block < previous {
			return fmt.Errorf("%w: descending block numbers", ErrInvalidMissingBlocks)
		}
		if emit != nil && (first || block != previous) {
			emit(block)
		}
		first, previous = false, block
	}
	return nil
}

func decodeMissingBlockInteger(data []byte) (uint64, []byte, error) {
	header := data[0]
	data = data[1:]
	if header>>5 != 0 {
		return 0, nil, fmt.Errorf("%w: expected unsigned integer", ErrInvalidMissingBlocks)
	}
	info := header & 0x1f
	if info < 24 {
		return uint64(info), data, nil
	}
	if info > 27 {
		return 0, nil, fmt.Errorf("%w: reserved integer encoding", ErrInvalidMissingBlocks)
	}
	width := 1 << (info - 24)
	if len(data) < width {
		return 0, nil, fmt.Errorf("%w: truncated integer", ErrInvalidMissingBlocks)
	}
	var number uint64
	switch width {
	case 1:
		number = uint64(data[0])
	case 2:
		number = uint64(binary.BigEndian.Uint16(data))
	case 4:
		number = uint64(binary.BigEndian.Uint32(data))
	case 8:
		number = binary.BigEndian.Uint64(data)
	}
	return number, data[width:], nil
}
