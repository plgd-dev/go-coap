package message

import "strings"

type Options []Option

const maxPathValue = 255

func (options Options) SetPath(buf []byte, path string) (Options, int, ErrorCode) {
	if len(path) == 0 {
		return options, 0, OK
	}
	o := options.Remove(URIPath)
	if path[0] == '/' {
		path = path[1:]
	}
	encoded := 0
	for start := 0; start < len(path); {
		subPath := path[start:]
		end := strings.Index(subPath, "/")
		if end <= 0 {
			end = len(subPath)
		}
		data := buf[encoded:]
		o, enc, err := o.AddOptionString(data, URIPath, subPath[:end])
		if err 
		enc, err := EncodeRunes(data, []rune(subPath[:end]))
		if err != OK {
			return options, -1, err
		}
		if enc > maxPathValue {
			return options, -1, ErrorCodeInvalidValueLength
		}
		encoded += enc
		o = o.Add(Option{ID: URIPath, Value: data[:enc]})
		start = start + end + 1
	}
	return o, encoded, OK
}

func (options Options) Path(buf []rune) (int, ErrorCode) {
	firstIdx, lastIdx, err := options.Find(URIPath)
	if err != OK {
		return -1, err
	}
	used := 0
	for i := firstIdx; i < lastIdx; i++ {
		if i != firstIdx {
			if len(buf) <= used {
				return -1, ErrorCodeTooSmall
			}
			buf[used] = '/'
			used++
		}
		len, err := DecodeRunes(buf[used:], options[i].Value)
		if err != OK {
			return -1, err
		}
		used += len
	}
	return used, OK
}

func (options Options) OptionUint32(id OptionID) (uint32, ErrorCode) {
	firstIdx, lastIdx, err := options.Find(Block1)
	if err != OK {
		return 0, err
	}
	if firstIdx != lastIdx {
		return 0, ErrorCodeOptionDuplicate
	}
	val, _, err := DecodeUint32(options[firstIdx].Value)
	return val, err
}

func (options Options) AddOptionString(buf []byte, id OptionID, str string) (Options, int, ErrorCode) {
	enc, err := EncodeRunes(buf, []rune(str))
	if err != OK {
		return options, -1, err
	}
	if id == URIPath && enc > maxPathValue {
		return options, -1, ErrorCodeInvalidValueLength
	}
	return options.Add(Option{ID: URIPath, Value: buf[:enc]}), enc, OK
}

func (options Options) SetOptionUint32(buf []byte, id OptionID, value uint32) (Options, int, ErrorCode) {
	enc, err := EncodeUint32(buf, uint32(value))
	if err != OK {
		return options, -1, err
	}
	o := options.Set(Option{ID: id, Value: buf[:enc]})
	return o, enc, err
}

func (options Options) SetContentFormat(buf []byte, contentFormat MediaType) (Options, int, ErrorCode) {
	return options.SetOptionUint32(buf, ContentFormat, uint32(contentFormat))
}

func (options Options) Find(ID OptionID) (int, int, ErrorCode) {
	idxPre := options.findPositon(ID, true)
	idxPost := options.findPositon(ID, false)
	if idxPre == idxPost {
		return -1, -1, ErrorCodeOptionNotFound
	}
	if ID == options[idxPre].ID {
		return idxPre, idxPost, OK
	}
	return idxPre + 1, idxPost, OK
}

func (options Options) findPositon(ID OptionID, prepend bool) int {
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

func (options Options) Set(opt Option) Options {
	idxPre := options.findPositon(opt.ID, true)
	idxPost := options.findPositon(opt.ID, false)

	//append
	if idxPre == idxPost {
		options = append(options, Option{})
		options[len(options)-1] = opt
		return options
	}
	//replace
	if idxPre+1 == idxPost {
		options[idxPre] = opt
		return options
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

	return options
}

func (options Options) Add(opt Option) Options {
	idx := options.findPositon(opt.ID, false)
	if len(options) == cap(options) {
		options = append(options, Option{})
	}
	options = options[:len(options)+1]
	for i := len(options) - 1; i > idx; i-- {
		options[i] = options[i-1]
	}
	options[idx] = opt
	return options
}

func (options Options) Remove(ID OptionID) Options {
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

func (m Options) Marshal(buf []byte) (int, ErrorCode) {
	previousID := OptionID(0)
	length := 0

	for _, o := range m {

		//return coap.ErrorCode but calculate length
		if length > len(buf) {
			buf = nil
		}

		var optionLength int
		var err ErrorCode

		if buf != nil {
			optionLength, err = o.Marshal(buf[length:], previousID)
		} else {
			optionLength, err = o.Marshal(nil, previousID)
		}
		previousID = o.ID

		switch err {
		case OK:
		case ErrorCodeTooSmall:
			buf = nil
		default:
			return -1, err
		}
		length = length + optionLength
	}
	if buf == nil {
		return length, ErrorCodeTooSmall
	}
	return length, OK
}

func (m *Options) Unmarshal(data []byte, optionDefs map[OptionID]OptionDef) (int, ErrorCode) {
	prev := 0
	processed := 0
	for len(data) > 0 {
		if data[0] == 0xff {
			processed++
			break
		}

		delta := int(data[0] >> 4)
		length := int(data[0] & 0x0f)

		if delta == ExtendOptionError || length == ExtendOptionError {
			return -1, ErrorCodeOptionUnexpectedExtendMarker
		}

		data = data[1:]
		processed++

		proc, delta, err := parseExtOpt(data, delta)
		if err != OK {
			return -1, err
		}
		processed += proc
		data = data[proc:]
		proc, length, err = parseExtOpt(data, length)
		if err != OK {
			return -1, err
		}
		processed += proc
		data = data[proc:]

		if len(data) < length {
			return -1, ErrorCodeOptionTruncated
		}

		option := Option{}
		oid := OptionID(prev + delta)
		proc, err = option.Unmarshal(data[:length], optionDefs, oid)
		if err != OK {
			return -1, err
		}

		if cap(*m) == len(*m) {
			return -1, ErrorCodeBytesOptionsTooSmall
		}
		(*m) = append(*m, []Option{option}...)

		processed += proc
		data = data[proc:]
		prev = int(oid)
	}

	return processed, OK
}
