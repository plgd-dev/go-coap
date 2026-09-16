package qblock

import (
	"encoding/binary"
	"errors"
	"math"

	coapMath "github.com/plgd-dev/go-coap/v3/pkg/math"
)

var errInvalidMissingSequence = errors.New("invalid missing-block sequence")

// DecodeMissing decodes a CBOR sequence of missing block numbers.
func DecodeMissing(data []byte, blockCount uint32, maxItems int) ([]uint32, error) {
	if len(data) == 0 || maxItems <= 0 {
		return nil, errInvalidMissingSequence
	}
	cursor := data
	var previous uint32
	havePrevious := false
	items := 0
	unique := 0
	for len(cursor) > 0 {
		items++
		if items > maxItems {
			return nil, errInvalidMissingSequence
		}
		number, consumed, err := decodeMissingNumber(cursor)
		if err != nil || number >= uint64(blockCount) {
			return nil, errInvalidMissingSequence
		}
		// decodeMissingNumber rejects values above math.MaxUint32.
		current := coapMath.CastTo[uint32](number)
		if havePrevious && current < previous {
			return nil, errInvalidMissingSequence
		}
		if !havePrevious || current != previous {
			unique++
		}
		previous = current
		havePrevious = true
		cursor = cursor[consumed:]
	}

	result := make([]uint32, 0, unique)
	havePrevious = false
	for len(data) > 0 {
		number, consumed, _ := decodeMissingNumber(data)
		// The validation pass above proved every decoded value fits uint32.
		current := coapMath.CastTo[uint32](number)
		if !havePrevious || current != previous {
			result = append(result, current)
		}
		previous = current
		havePrevious = true
		data = data[consumed:]
	}
	return result, nil
}

func decodeMissingNumber(data []byte) (uint64, int, error) {
	if len(data) == 0 || data[0]>>5 != 0 {
		return 0, 0, errInvalidMissingSequence
	}
	switch additional := data[0] & 0x1f; {
	case additional < 24:
		return uint64(additional), 1, nil
	case additional == 24:
		if len(data) < 2 {
			return 0, 0, errInvalidMissingSequence
		}
		return uint64(data[1]), 2, nil
	case additional == 25:
		if len(data) < 3 {
			return 0, 0, errInvalidMissingSequence
		}
		return uint64(binary.BigEndian.Uint16(data[1:3])), 3, nil
	case additional == 26:
		if len(data) < 5 {
			return 0, 0, errInvalidMissingSequence
		}
		return uint64(binary.BigEndian.Uint32(data[1:5])), 5, nil
	case additional == 27:
		if len(data) < 9 {
			return 0, 0, errInvalidMissingSequence
		}
		number := binary.BigEndian.Uint64(data[1:9])
		if number > math.MaxUint32 {
			return 0, 0, errInvalidMissingSequence
		}
		return number, 9, nil
	default:
		return 0, 0, errInvalidMissingSequence
	}
}

// EncodeMissing encodes the largest complete prefix that fits maxBytes.
func EncodeMissing(numbers []uint32, maxBytes int) (payload []byte, consumed int, err error) {
	if len(numbers) == 0 || maxBytes <= 0 {
		return nil, 0, errInvalidMissingSequence
	}
	for i := 1; i < len(numbers); i++ {
		if numbers[i] <= numbers[i-1] {
			return nil, 0, errInvalidMissingSequence
		}
	}
	payload = make([]byte, 0, min(maxBytes, len(numbers)*5))
	for _, number := range numbers {
		encoded := encodeMissingNumber(number)
		if len(payload)+len(encoded) > maxBytes {
			break
		}
		payload = append(payload, encoded...)
		consumed++
	}
	if consumed == 0 {
		return nil, 0, errInvalidMissingSequence
	}
	return payload, consumed, nil
}

func encodeMissingNumber(number uint32) []byte {
	switch {
	case number < 24:
		return []byte{byte(number)}
	case number <= math.MaxUint8:
		return []byte{0x18, byte(number)}
	case number <= math.MaxUint16:
		encoded := []byte{0x19, 0, 0}
		// This branch bounds number to uint16.
		binary.BigEndian.PutUint16(encoded[1:], coapMath.CastTo[uint16](number))
		return encoded
	default:
		encoded := []byte{0x1a, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(encoded[1:], number)
		return encoded
	}
}
