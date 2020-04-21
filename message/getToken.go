package message

import (
	"crypto/rand"
	"encoding/binary"
	"hash/crc64"
)

// GetToken generates a random token by a given length
func GetToken() ([]byte, error) {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}

// Calculate ETag from payload via CRC64
func CalcETag(payload []byte) []byte {
	if payload != nil {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, crc64.Checksum(payload, crc64.MakeTable(crc64.ISO)))
		return b
	}
	return nil
}
