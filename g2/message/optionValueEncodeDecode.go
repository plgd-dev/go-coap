package message

import (
	"encoding/binary"
	"unicode/utf8"
)

func EncodeRunes(p []byte, value []rune) (int, ErrorCode) {
	encoded := 0
	tmpBuf := make([]byte, 8)
	useTmpBuf := false
	for _, r := range value {
		if useTmpBuf {
			encoded += utf8.EncodeRune(tmpBuf, r)
		} else {
			if utf8.RuneLen(r) > len(p[encoded:]) {
				useTmpBuf = true
				encoded += utf8.EncodeRune(tmpBuf, r)
			} else {
				encoded += utf8.EncodeRune(p, r)
			}
		}
	}
	if useTmpBuf {
		return encoded, ErrorCodeTooSmall
	}
	return encoded, OK
}

func DecodeRunes(buf []rune, p []byte) (decoded int, err ErrorCode) {
	idx := 0
	err = OK
	for {
		r, size := utf8.DecodeRune(p[decoded:])
		if r == utf8.RuneError && size == 0 {
			return
		}
		if r == utf8.RuneError && size == 1 {
			return -1, ErrorCodeInvalidEncoding
		}
		decoded += size
		if idx < len(buf) {
			buf[idx] = r
			idx++
		} else {
			err = ErrorCodeTooSmall
		}
	}
}

func EncodeUint32(buf []byte, value uint32) (int, ErrorCode) {
	switch {
	case value == 0:
		return 0, OK
	case value <= max1ByteNumber:
		if len(buf) < 1 {
			return 1, ErrorCodeTooSmall
		}
		buf[0] = byte(value)
		return 1, OK
	case value <= max2ByteNumber:
		if len(buf) < 2 {
			return 2, ErrorCodeTooSmall
		}
		binary.BigEndian.PutUint16(buf, uint16(value))
		return 2, OK
	case value <= max3ByteNumber:
		if len(buf) < 3 {
			return 3, ErrorCodeTooSmall
		}
		rv := make([]byte, 4)
		binary.BigEndian.PutUint32(rv[:], value)
		copy(buf, rv[1:])
		return 3, OK
	default:
		if len(buf) < 4 {
			return 4, ErrorCodeTooSmall
		}
		binary.BigEndian.PutUint32(buf, value)
		return 4, OK
	}
}

func DecodeUint32(buf []byte) (uint32, int, ErrorCode) {
	if len(buf) > 4 {
		buf = buf[:4]
	}
	tmp := []byte{0, 0, 0, 0}
	copy(tmp[4-len(buf):], buf)
	value := binary.BigEndian.Uint32(tmp)
	return value, len(buf), OK
}
