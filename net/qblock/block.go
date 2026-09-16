package qblock

import "github.com/plgd-dev/go-coap/v3/net/blockwise"

const (
	maxBlockNumber = uint32(1<<20 - 1)
	maxBlockValue  = uint32(1<<24 - 1)
)

// Block is the decoded value of a Q-Block option.
type Block struct {
	Number uint32
	More   bool
	SZX    blockwise.SZX
}

// EncodeBlock encodes a Q-Block option value.
func EncodeBlock(b Block) (uint32, error) {
	if b.Number > maxBlockNumber {
		return 0, blockwise.ErrBlockNumberExceedLimit
	}
	if b.SZX > blockwise.SZX1024 {
		return 0, blockwise.ErrInvalidSZX
	}
	value := b.Number << 4
	if b.More {
		value |= 1 << 3
	}
	return value | uint32(b.SZX), nil
}

// DecodeBlock decodes a Q-Block option value.
func DecodeBlock(value uint32) (Block, error) {
	if value > maxBlockValue {
		return Block{}, blockwise.ErrBlockInvalidSize
	}
	szx := blockwise.SZX(value & 0x7)
	if szx > blockwise.SZX1024 {
		return Block{}, blockwise.ErrInvalidSZX
	}
	return Block{
		Number: value >> 4,
		More:   value&(1<<3) != 0,
		SZX:    szx,
	}, nil
}
