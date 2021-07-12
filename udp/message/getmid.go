package message

import (
	"crypto/rand"
	"encoding/binary"
	"sync/atomic"
)

var msgID = uint32(RandMID())

// GetMID generates a message id for UDP-coap
func GetMID() uint16 {
	return uint16(atomic.AddUint32(&msgID, 1))
}

func RandMID() uint16 {
	b := make([]byte, 4)
	rand.Read(b)
	return uint16(binary.BigEndian.Uint32(b))
}
