package qblock

import (
	"encoding/binary"
	"errors"
	"math"
)

// DecodeMissing reads a CBOR sequence of unsigned missing-block numbers.
func DecodeMissing(data []byte, blockCount uint32, maxItems int) ([]uint32, error) {
	if len(data) == 0 || maxItems <= 0 {
		return nil, errors.New("empty or unbounded missing-block sequence")
	}
	var numbers []uint32
	var previous uint32
	items := 0
	for len(data) > 0 {
		items++
		if items > maxItems {
			return nil, errors.New("missing-block item limit exceeded")
		}
		value, consumed, err := decodeUnsigned(data)
		if err != nil {
			return nil, err
		}
		if value > math.MaxUint32 || value >= uint64(blockCount) {
			return nil, errors.New("missing-block number outside body")
		}
		number := uint32(value)
		if len(numbers) > 0 && number < previous {
			return nil, errors.New("descending missing-block sequence")
		}
		if len(numbers) == 0 || number != previous {
			numbers = append(numbers, number)
		}
		previous = number
		data = data[consumed:]
	}
	return numbers, nil
}

func decodeUnsigned(data []byte) (uint64, int, error) {
	if data[0]>>5 != 0 {
		return 0, 0, errors.New("missing-block item is not unsigned")
	}
	minor := data[0] & 0x1f
	switch {
	case minor < 24:
		return uint64(minor), 1, nil
	case minor == 24:
		if len(data) < 2 {
			break
		}
		return uint64(data[1]), 2, nil
	case minor == 25:
		if len(data) < 3 {
			break
		}
		return uint64(binary.BigEndian.Uint16(data[1:3])), 3, nil
	case minor == 26:
		if len(data) < 5 {
			break
		}
		return uint64(binary.BigEndian.Uint32(data[1:5])), 5, nil
	case minor == 27:
		if len(data) < 9 {
			break
		}
		return binary.BigEndian.Uint64(data[1:9]), 9, nil
	default:
		return 0, 0, errors.New("invalid CBOR unsigned integer width")
	}
	return 0, 0, errors.New("truncated CBOR unsigned integer")
}

// EncodeMissing writes the largest whole-number prefix that fits maxBytes.
func EncodeMissing(numbers []uint32, maxBytes int) ([]byte, int, error) {
	if len(numbers) == 0 || maxBytes <= 0 {
		return nil, 0, errors.New("empty missing-block sequence or byte budget")
	}
	for i := 1; i < len(numbers); i++ {
		if numbers[i] <= numbers[i-1] {
			return nil, 0, errors.New("missing-block sequence is not strictly increasing")
		}
	}
	var payload []byte
	for i, number := range numbers {
		var encoded [5]byte
		length := encodeUnsigned(encoded[:], number)
		if len(payload)+length > maxBytes {
			if i == 0 {
				return nil, 0, errors.New("first missing-block number exceeds byte budget")
			}
			return payload, i, nil
		}
		payload = append(payload, encoded[:length]...)
	}
	return payload, len(numbers), nil
}

func encodeUnsigned(dst []byte, number uint32) int {
	switch {
	case number < 24:
		dst[0] = byte(number)
		return 1
	case number <= math.MaxUint8:
		dst[0], dst[1] = 0x18, byte(number)
		return 2
	case number <= math.MaxUint16:
		dst[0] = 0x19
		binary.BigEndian.PutUint16(dst[1:3], uint16(number))
		return 3
	default:
		dst[0] = 0x1a
		binary.BigEndian.PutUint32(dst[1:5], number)
		return 5
	}
}
