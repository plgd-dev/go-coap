package tcpcoap

type BytesOption struct {
	ID    OptionID
	Value []byte
}

type BytesOptions []BytesOption

const maxPathValue = 255

func (o BytesOption) MarshalValue(buf []byte) (int, ErrorCode) {
	if len(buf) < len(o.Value) {
		return len(o.Value), ErrorCodeTooSmall
	}
	copy(buf, o.Value)
	return len(o.Value), OK
}

func (o *BytesOption) UnmarshalValue(buf []byte) (int, ErrorCode) {
	o.Value = buf
	return len(buf), OK
}

func (o BytesOption) Marshal(buf []byte, previousID OptionID) (int, ErrorCode) {
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

func (options BytesOptions) findPositon(ID OptionID, prepend bool) int {
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

func (options BytesOptions) Set(opt BytesOption) (BytesOptions, ErrorCode) {
	idxPre := options.findPositon(opt.ID, true)
	idxPost := options.findPositon(opt.ID, false)

	//append
	if idxPre == idxPost {
		if len(options) == cap(options) {
			return options, ErrorCodeTooSmall
		}
		options = options[:len(options)+1]
		options[len(options)-1] = opt
		return options, OK
	}
	//replace
	if idxPre+1 == idxPost {
		options[idxPre] = opt
		return options, OK
	}

	//replace + move
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

/*
func (options BytesOptions) SetPath(path string) (BytesOptions, ErrorCode) {
	if len(path) == 0 {
		return options, OK
	}
	o := options.Remove(URIPath)
	if path[0] == '/' {
		path = path[1:]
	}
	err := OK
	for _, p := range strings.Split(path, "/") {
		if len(p) > maxPathValue {
			return options, ErrorCodeInvalidValueLength
		}
		var buf [255]byte
		var bufPos int
		for idx, c := range p {
			len := EncodeRune(p []byte, r rune)
			buf[idx] = c
		}

		o, err = o.Add(BytesOption{ID: URIPath, Value: []byte(p)})
		if err != OK {
			return options, err
		}
	}
	return o, OK
}
*/

func (options BytesOptions) Add(opt BytesOption) (BytesOptions, ErrorCode) {
	if len(options) == cap(options) {
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

func (options BytesOptions) Remove(ID OptionID) BytesOptions {
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
