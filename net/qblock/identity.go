package qblock

import (
	"encoding/binary"
	"errors"

	coapMath "github.com/plgd-dev/go-coap/v3/pkg/math"
)

const (
	maxOperationKeyParts = 32
	maxOperationKeyBytes = 512
)

// OperationKey is an opaque, canonical identifier supplied by the adapter.
// Its bytes are never parsed by the manager.
type OperationKey string

// NewOperationKey builds an unambiguous key from nonempty byte parts.
func NewOperationKey(parts ...[]byte) (OperationKey, error) {
	if len(parts) == 0 || len(parts) > maxOperationKeyParts {
		return "", errors.New("invalid operation key part count")
	}

	size := 0
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxOperationKeyBytes {
			return "", errors.New("invalid operation key part")
		}
		size += 2 + len(part)
		if size > maxOperationKeyBytes {
			return "", errors.New("operation key exceeds limit")
		}
	}

	key := make([]byte, size)
	offset := 0
	for _, part := range parts {
		// The validation loop bounds every part to 512 bytes.
		binary.BigEndian.PutUint16(key[offset:], coapMath.CastTo[uint16](len(part)))
		offset += 2
		copy(key[offset:], part)
		offset += len(part)
	}
	return OperationKey(key), nil
}
