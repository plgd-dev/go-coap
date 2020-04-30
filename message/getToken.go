package message

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"hash/crc64"
)

type Token []byte

func (t Token) String() string {
	return base64.StdEncoding.EncodeToString(t)
}

// GetToken generates a random token by a given length
func GetToken() (Token, error) {
	b := make(Token, 8)
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
