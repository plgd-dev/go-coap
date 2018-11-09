package tcpcoap

import (
	"encoding/binary"
)

type Uint32Option struct {
	ID    OptionID
	Value uint32
}

type Uint32Options []Uint32Option

func (o Uint32Option) MarshalValue(buf []byte) (int, ErrorCode) {
	switch {
	case o.Value == 0:
		return 0, OK
	case o.Value <= max1ByteNumber:
		if len(buf) < 1 {
			return 1, ErrorCodeTooSmall
		}
		buf[0] = byte(o.Value)
		return 1, OK
	case o.Value <= max2ByteNumber:
		if len(buf) < 2 {
			return 2, ErrorCodeTooSmall
		}
		binary.BigEndian.PutUint16(buf, uint16(o.Value))
		return 2, OK
	case o.Value <= max3ByteNumber:
		if len(buf) < 3 {
			return 3, ErrorCodeTooSmall
		}
		rv := make([]byte, 4)
		binary.BigEndian.PutUint32(rv[:], o.Value)
		copy(buf, rv[1:])
		return 3, OK
	default:
		if len(buf) < 4 {
			return 4, ErrorCodeTooSmall
		}
		binary.BigEndian.PutUint32(buf, o.Value)
		return 4, OK
	}
}

func (o *Uint32Option) UnmarshalValue(buf []byte) (int, ErrorCode) {
	if len(buf) > 4 {
		buf = buf[:4]
	}
	tmp := []byte{0, 0, 0, 0}
	copy(tmp[4-len(buf):], buf)
	o.Value = binary.BigEndian.Uint32(tmp)
	return len(buf), OK
}

func (o Uint32Option) Marshal(buf []byte, previousID OptionID) (int, ErrorCode) {
	/*
	     0   1   2   3   4   5   6   7
	   +---------------+---------------+
	   |               |               |
	   |  Option Delta | Option Length |   1 byte
	   |               |               |
	   +---------------+---------------+
	   \                               \
	   /         Option Delta          /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /         Option Length         /   0-2 bytes
	   \          (extended)           \
	   +-------------------------------+
	   \                               \
	   /                               /
	   \                               \
	   /         Option Value          /   0 or more bytes
	   \                               \
	   /                               /
	   \                               \
	   +-------------------------------+
	*/
	delta := int(o.ID) - int(previousID)

	lenBuf, err := o.MarshalValue(nil)
	switch err {
	case ErrorCodeTooSmall, OK:
	default:
		return -1, err
	}

	//header marshal
	lenBuf, err = marshalOptionHeader(buf, delta, lenBuf)
	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	length := lenBuf

	if buf == nil {
		lenBuf, err = o.MarshalValue(nil)
	} else {
		lenBuf, err = o.MarshalValue(buf[length:])
	}

	switch err {
	case OK:
	case ErrorCodeTooSmall:
		buf = nil
	default:
		return -1, err
	}
	length = length + lenBuf

	if buf == nil {
		return length, ErrorCodeTooSmall
	}
	return length, OK
}

func (options Uint32Options) findPositon(ID OptionID, prepend bool) int {
	if len(options) == 0 {
		return 0
	}
	pivot := 0
	maxIdx := len(options)
	minIdx := 0
	for {
		move := (maxIdx - minIdx) / 2
		switch {
		case ID < options[pivot].ID:
			if pivot == 0 {
				return 0
			}
			maxIdx = pivot
			if move == 0 {
				return pivot
			}
			if pivot-move < 0 {
				pivot = 0
			} else {
				pivot = pivot - move
			}
		case ID == options[pivot].ID:
			if move == 0 {
				if prepend {
					return pivot
				}
				return pivot + 1
			}
			if prepend {
				maxIdx = pivot
				if pivot-move < 0 {
					pivot = 0
				} else {
					pivot = pivot - move
				}
			} else {
				minIdx = pivot
				if pivot+move >= len(options) {
					pivot = len(options) - 1
				} else {
					pivot = pivot + move
				}
			}
		case ID > options[pivot].ID:
			if pivot == len(options)-1 {
				return pivot + 1
			}
			minIdx = pivot
			if move == 0 {
				return pivot + 1
			}
			if pivot+move >= len(options) {
				pivot = len(options) - 1
			} else {
				pivot = pivot + move
			}
		}
	}
}

func (options Uint32Options) Set(opt Uint32Option) (Uint32Options, ErrorCode) {
	idxPre := options.findPositon(opt.ID, true)
	idxPost := options.findPositon(opt.ID, false)

	if idxPre == idxPost {
		if cap(options) == len(options) {
			return options, ErrorCodeTooSmall
		}
		options = options[:len(options)+1]
		options[len(options)-1] = opt
		return options, OK
	}
	if idxPre+1 == idxPost {
		options[idxPre] = opt
		return options, OK
	}

	options[idxPre] = opt
	updateIdx := idxPre + 1
	for i := idxPost; i < len(options); i++ {
		options[updateIdx] = options[i]
		updateIdx++
	}
	length := len(options) - (idxPost - 1 - idxPre)
	options = options[:length]

	return options, OK
}

func (options Uint32Options) SetContentFormat(contentFormat MediaType) (Uint32Options, ErrorCode) {
	return options.Set(Uint32Option{ID: ContentFormat, Value: uint32(contentFormat)})
}

func (options Uint32Options) Add(opt Uint32Option) (Uint32Options, ErrorCode) {
	if cap(options) == len(options) {
		return options, ErrorCodeTooSmall
	}
	idx := options.findPositon(opt.ID, false)
	options = options[:len(options)+1]
	for i := len(options) - 1; i > idx; i-- {
		options[i] = options[i-1]
	}
	options[idx] = opt
	return options, OK
}

func (options Uint32Options) Remove(ID OptionID) Uint32Options {
	idxPre := options.findPositon(ID, true)
	idxPost := options.findPositon(ID, false)
	if idxPre == idxPost {
		return options
	}

	updateIdx := idxPre
	for i := idxPost; i < len(options); i++ {
		options[updateIdx] = options[i]
		updateIdx++
	}
	length := len(options) - (idxPost - idxPre)
	options = options[:length]

	return options
}
