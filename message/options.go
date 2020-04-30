package message

import (
	"strings"
)

type Options []Option

const maxPathValue = 255

func (options Options) SetPath(buf []byte, path string) (Options, int, error) {
	if len(path) == 0 {
		return options, 0, nil
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
		var enc int
		var err error
		o, enc, err = o.AddOptionString(data, URIPath, subPath[:end])
		if err != nil {
			return o, -1, err
		}
		encoded += enc
		start = start + end + 1
	}
	return o, encoded, nil
}

func (options Options) Path(buf []byte) (int, error) {
	firstIdx, lastIdx, err := options.Find(URIPath)
	if err != nil {
		return -1, err
	}
	var needed int
	for i := firstIdx; i < lastIdx; i++ {
		needed += len(options[i].Value)
		needed++
	}
	needed--
	if len(buf) < needed {
		return needed, ErrTooSmall
	}
	for i := firstIdx; i < lastIdx; i++ {
		if i != firstIdx {
			buf[0] = '/'
			buf = buf[1:]
		}
		copy(buf, options[i].Value)
		buf = buf[len(options[i].Value):]
	}
	return needed, nil
}

func (options Options) SetOptionString(buf []byte, id OptionID, str string) (Options, int, error) {
	data := []byte(str)
	return options.SetOptionBytes(buf, id, data)
}

func (options Options) AddOptionString(buf []byte, id OptionID, str string) (Options, int, error) {
	data := []byte(str)
	return options.AddOptionBytes(buf, id, data)
}

func (options Options) HasOption(id OptionID) bool {
	_, _, err := options.Find(id)
	return err == nil
}

func (options Options) ReadUint32(id OptionID, r []uint32) (int, error) {
	firstIdx, lastIdx, err := options.Find(id)
	if err != nil {
		return 0, err
	}
	if len(r) < lastIdx-firstIdx {
		return lastIdx - firstIdx, ErrShortRead
	}
	var idx int
	for i := firstIdx; i <= lastIdx; i++ {
		val, _, err := DecodeUint32(options[i].Value)
		if err == nil {
			r[idx] = val
			idx++
		}
	}

	return idx, nil
}

func (options Options) GetOptionUint32(id OptionID) (uint32, error) {
	firstIdx, _, err := options.Find(id)
	if err != nil {
		return 0, err
	}
	val, _, err := DecodeUint32(options[firstIdx].Value)
	return val, err
}

func (options Options) GetString(id OptionID) (string, error) {
	firstIdx, _, err := options.Find(id)
	if err != nil {
		return "", err
	}
	return string(options[firstIdx].Value), nil
}

func (options Options) SetOptionBytes(buf []byte, id OptionID, data []byte) (Options, int, error) {
	if len(buf) < len(data) {
		return options, len(data), ErrTooSmall
	}
	if id == URIPath && len(data) > maxPathValue {
		return options, -1, ErrInvalidValueLength
	}
	copy(buf, data)
	return options.Set(Option{ID: URIPath, Value: buf[:len(data)]}), len(data), nil
}

func (options Options) AddOptionBytes(buf []byte, id OptionID, data []byte) (Options, int, error) {
	if len(buf) < len(data) {
		return options, len(data), ErrTooSmall
	}
	if id == URIPath && len(data) > maxPathValue {
		return options, -1, ErrInvalidValueLength
	}
	copy(buf, data)
	return options.Add(Option{ID: URIPath, Value: buf[:len(data)]}), len(data), nil
}

func (options Options) GetOptionBytes(id OptionID) ([]byte, error) {
	firstIdx, _, err := options.Find(id)
	if err != nil {
		return nil, err
	}
	return options[firstIdx].Value, nil
}

func (options Options) ReadStrings(id OptionID, r []string) (int, error) {
	firstIdx, lastIdx, err := options.Find(id)
	if err != nil {
		return 0, err
	}
	if len(r) < lastIdx-firstIdx {
		return lastIdx - firstIdx, ErrShortRead
	}
	var idx int
	for i := firstIdx; i < lastIdx; i++ {
		r[idx] = string(options[i].Value)
		idx++
	}

	return idx, nil
}

func (options Options) ReadBytes(id OptionID, r [][]byte) (int, error) {
	firstIdx, lastIdx, err := options.Find(id)
	if err != nil {
		return 0, err
	}
	if len(r) < lastIdx-firstIdx {
		return lastIdx - firstIdx, ErrShortRead
	}
	var idx int
	for i := firstIdx; i < lastIdx; i++ {
		r[idx] = options[i].Value
		idx++
	}

	return idx, nil
}

func (options Options) AddOptionUint32(buf []byte, id OptionID, value uint32) (Options, int, error) {
	enc, err := EncodeUint32(buf, uint32(value))
	if err != nil {
		return options, -1, err
	}
	o := options.Add(Option{ID: id, Value: buf[:enc]})
	return o, enc, err
}

func (options Options) SetOptionUint32(buf []byte, id OptionID, value uint32) (Options, int, error) {
	enc, err := EncodeUint32(buf, uint32(value))
	if err != nil {
		return options, -1, err
	}
	o := options.Set(Option{ID: id, Value: buf[:enc]})
	return o, enc, err
}

func (options Options) SetContentFormat(buf []byte, contentFormat MediaType) (Options, int, error) {
	return options.SetOptionUint32(buf, ContentFormat, uint32(contentFormat))
}

func (options Options) Find(ID OptionID) (int, int, error) {
	idxPre, idxPost := options.findPositon(ID)
	if idxPre == -1 && idxPost == 0 {
		return -1, -1, ErrOptionNotFound
	}
	if idxPre == len(options)-1 && idxPost == -1 {
		return -1, -1, ErrOptionNotFound
	}
	if idxPre < idxPost && idxPost-idxPre == 1 {
		return -1, -1, ErrOptionNotFound
	}
	idxPre = idxPre + 1
	if idxPost < 0 {
		idxPost = len(options)
	}
	return idxPre, idxPost, nil
}

// findPositon returns opened interval, -1 at means minIdx insert at 0, -1 maxIdx at maxIdx means append.
func (options Options) findPositon(ID OptionID) (minIdx int, maxIdx int) {
	if len(options) == 0 {
		return -1, 0
	}
	pivot := 0
	maxIdx = len(options)
	minIdx = 0
	for {
		switch {
		case ID == options[pivot].ID || (maxIdx-minIdx)/2 == 0:
			for maxIdx = pivot; maxIdx < len(options) && options[maxIdx].ID <= ID; maxIdx++ {
			}
			if maxIdx == len(options) {
				maxIdx = -1
			}
			for minIdx = pivot; minIdx >= 0 && options[minIdx].ID >= ID; minIdx-- {
			}
			return minIdx, maxIdx
		case ID < options[pivot].ID:
			maxIdx = pivot
			pivot = maxIdx - (maxIdx-minIdx)/2
		case ID > options[pivot].ID:
			minIdx = pivot
			pivot = minIdx + (maxIdx-minIdx)/2
		}
	}
}

func (options Options) Set(opt Option) Options {
	idxPre, idxPost := options.findPositon(opt.ID)
	if idxPre == -1 && idxPost == -1 {
		//append
		options = append(options[:0], opt)
		return options
	}
	var insertPosition int
	var updateTo int
	var updateFrom int
	optsLength := len(options)
	switch {
	case idxPre == -1 && idxPost >= 0:
		insertPosition = 0
		updateTo = 1
		updateFrom = idxPost
	case idxPre == idxPost:
		insertPosition = idxPre
		updateFrom = idxPre
		updateTo = idxPre + 1
	case idxPre >= 0:
		insertPosition = idxPre + 1
		updateTo = idxPre + 2
		updateFrom = idxPost
		if updateFrom < 0 {
			updateFrom = len(options)
		}
		if updateTo == updateFrom {
			options[insertPosition] = opt
			return options
		}
	}
	if len(options) == cap(options) {
		options = append(options, Option{})
	} else {
		options = options[:len(options)+1]
	}
	//replace + move
	updateIdx := updateTo
	if updateFrom < updateTo {
		for i := optsLength; i > updateFrom; i-- {
			options[i] = options[i-1]
			updateIdx++
		}
	} else {
		for i := updateFrom; i < optsLength; i++ {
			options[updateIdx] = options[i]
			updateIdx++
		}
	}
	options[insertPosition] = opt
	options = options[:updateIdx]

	return options
}

func (options Options) Add(opt Option) Options {
	_, idxPost := options.findPositon(opt.ID)
	if idxPost == -1 {
		idxPost = len(options)
	}
	if len(options) == cap(options) {
		options = append(options, Option{})
	} else {
		options = options[:len(options)+1]
	}
	for i := len(options) - 1; i > idxPost; i-- {
		options[i] = options[i-1]
	}
	options[idxPost] = opt
	return options
}

func (options Options) Remove(ID OptionID) Options {
	idxPre, idxPost, err := options.Find(ID)
	if err != nil {
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

func (m Options) Marshal(buf []byte) (int, error) {
	previousID := OptionID(0)
	length := 0

	for _, o := range m {

		//return coap.error but calculate length
		if length > len(buf) {
			buf = nil
		}

		var optionLength int
		var err error

		if buf != nil {
			optionLength, err = o.Marshal(buf[length:], previousID)
		} else {
			optionLength, err = o.Marshal(nil, previousID)
		}
		previousID = o.ID

		switch err {
		case nil:
		case ErrTooSmall:
			buf = nil
		default:
			return -1, err
		}
		length = length + optionLength
	}
	if buf == nil {
		return length, ErrTooSmall
	}
	return length, nil
}

func (m *Options) Unmarshal(data []byte, optionDefs map[OptionID]OptionDef) (int, error) {
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
			return -1, ErrOptionUnexpectedExtendMarker
		}

		data = data[1:]
		processed++

		proc, delta, err := parseExtOpt(data, delta)
		if err != nil {
			return -1, err
		}
		processed += proc
		data = data[proc:]
		proc, length, err = parseExtOpt(data, length)
		if err != nil {
			return -1, err
		}
		processed += proc
		data = data[proc:]

		if len(data) < length {
			return -1, ErrOptionTruncated
		}

		option := Option{}
		oid := OptionID(prev + delta)
		proc, err = option.Unmarshal(data[:length], optionDefs, oid)
		if err != nil {
			return -1, err
		}

		if cap(*m) == len(*m) {
			return -1, ErrBytesOptionsTooSmall
		}
		if option.ID != 0 {
			(*m) = append(*m, option)
		}

		processed += proc
		data = data[proc:]
		prev = int(oid)
	}

	return processed, nil
}
