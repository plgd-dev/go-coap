package coap

import (
	"crypto/rand"
	"sync/atomic"
)

// GenerateToken generates a random token by a given length
func GenerateToken(n int) ([]byte, error) {
	if n == 0 || n > 8 {
		return nil, ErrInvalidTokenLen
	}
	b := make([]byte, n)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}

var msgIdIter = uint32(0)

// GenerateMessageId generates a message id for UDP-coap
func GenerateMessageId() uint16 {
	return uint16(atomic.AddUint32(&msgIdIter, 1) % 0xffff)
}
