package blockwise

import (
	"errors"

	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
)

var (
	// ErrBlockNumberExceedLimit block number exceeded the limit 1,048,576
	ErrBlockNumberExceedLimit = blocks.ErrBlockNumberExceedLimit

	// ErrBlockInvalidSize block has invalid size
	ErrBlockInvalidSize = blocks.ErrBlockInvalidSize

	// ErrInvalidOptionBlock2 message has invalid value of Block2
	ErrInvalidOptionBlock2 = blocks.ErrInvalidOptionBlock2

	// ErrInvalidOptionBlock1 message has invalid value of Block1
	ErrInvalidOptionBlock1 = blocks.ErrInvalidOptionBlock1

	// ErrInvalidResponseCode response code has invalid value
	ErrInvalidResponseCode = errors.New("response code has invalid value")

	// ErrInvalidPayloadSize invalid payload size
	ErrInvalidPayloadSize = errors.New("invalid payload size")

	// ErrInvalidSZX invalid block-wise transfer szx
	ErrInvalidSZX = blocks.ErrInvalidSZX

	// ErrRequestEntityIncomplete request body is missing blocks
	ErrRequestEntityIncomplete = errors.New("request entity incomplete")

	// ErrRequestEntityTooLarge request body exceeds the size limit
	ErrRequestEntityTooLarge = blocks.ErrRequestEntityTooLarge
)
