package qblock

import (
	"fmt"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	pkgMath "github.com/plgd-dev/go-coap/v3/pkg/math"
)

const maxBlockNumber = 1048575

// Block is the NUM, M, and SZX fields of a Q-Block option.
type Block struct {
	Number uint32
	More   bool
	SZX    blockwise.SZX
}

func EncodeBlock(b Block) (uint32, error) {
	if b.Number > maxBlockNumber {
		return 0, fmt.Errorf("Q-Block number %d exceeds 20 bits", b.Number)
	}
	if b.SZX > blockwise.SZX1024 {
		return 0, fmt.Errorf("Q-Block SZX %d is not supported", b.SZX)
	}
	return blockwise.EncodeBlockOption(b.SZX, int64(b.Number), b.More)
}

func DecodeBlock(value uint32) (Block, error) {
	szx, number, more, err := blockwise.DecodeBlockOption(value)
	if err != nil {
		return Block{}, err
	}
	if szx > blockwise.SZX1024 {
		return Block{}, fmt.Errorf("Q-Block SZX %d is not supported", szx)
	}
	return Block{Number: pkgMath.CastTo[uint32](number), More: more, SZX: szx}, nil
}
