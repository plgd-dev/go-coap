// Package blocks contains wire types shared by the legacy and Q-Block engines.
package blocks

import "errors"

// SZX represents the block size exponent in a Block or Q-Block option.
type SZX uint8

const (
	SZX16 SZX = iota
	SZX32
	SZX64
	SZX128
	SZX256
	SZX512
	SZX1024
	SZXBERT
)

// Size returns the block size in bytes, or -1 for an invalid exponent.
func (s SZX) Size() int64 {
	if s > SZXBERT {
		return -1
	}
	return int64(16) << min(s, SZX1024)
}

var (
	ErrBlockNumberExceedLimit = errors.New("block number exceeded the limit 1,048,576")
	ErrBlockInvalidSize       = errors.New("block has invalid size")
	ErrInvalidOptionBlock2    = errors.New("message has invalid value of Block2")
	ErrInvalidOptionBlock1    = errors.New("message has invalid value of Block1")
	ErrInvalidSZX             = errors.New("invalid block-wise transfer szx")
	ErrRequestEntityTooLarge  = errors.New("request entity too large")
)
