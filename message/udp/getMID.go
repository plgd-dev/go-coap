package udp

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
)

var msgIDIter uint32

func init() {
	b := make([]byte, 4)
	n, err := rand.Read(b)
	if err == nil && n == len(b) {
		msgIDIter = binary.BigEndian.Uint32(b)
	}
}

// GetMID generates a message id for UDP-coap
func GetMID() uint16 {
	return uint16(atomic.AddUint32(&msgIDIter, 1) % 0xffff)
}
