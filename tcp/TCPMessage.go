package tcpcoap

import (
	"encoding/binary"
)

const (
	TCP_MESSAGE_LEN13_BASE = 13
	TCP_MESSAGE_LEN14_BASE = 269
	TCP_MESSAGE_LEN15_BASE = 65805
	TCP_MESSAGE_MAX_LEN    = 0x7fff0000 // Large number that works in 32-bit builds.
)

// Signal CSM Option IDs
/*
   +-----+---+---+-------------------+--------+--------+---------+
   | No. | C | R | Name              | Format | Length | Default |
   +-----+---+---+-------------------+--------+--------+---------+
   |   2 |   |   | MaxMessageSize    | uint   | 0-4    | 1152    |
   |   4 |   |   | BlockWiseTransfer | empty  | 0      | (none)  |
   +-----+---+---+-------------------+--------+--------+---------+
   C=Critical, R=Repeatable
*/

const (
	MaxMessageSize    OptionID = 2
	BlockWiseTransfer OptionID = 4
)

// Signal Ping/Pong Option IDs
/*
   +-----+---+---+-------------------+--------+--------+---------+
   | No. | C | R | Name              | Format | Length | Default |
   +-----+---+---+-------------------+--------+--------+---------+
   |   2 |   |   | Custody           | empty  | 0      | (none)  |
   +-----+---+---+-------------------+--------+--------+---------+
   C=Critical, R=Repeatable
*/

const (
	Custody OptionID = 2
)

// Signal Release Option IDs
/*
   +-----+---+---+---------------------+--------+--------+---------+
   | No. | C | R | Name                | Format | Length | Default |
   +-----+---+---+---------------------+--------+--------+---------+
   |   2 |   | x | Alternative-Address | string | 1-255  | (none)  |
   |   4 |   |   | Hold-Off            | uint3  | 0-3    | (none)  |
   +-----+---+---+---------------------+--------+--------+---------+
   C=Critical, R=Repeatable
*/

const (
	AlternativeAddress OptionID = 2
	HoldOff            OptionID = 4
)

// Signal Abort Option IDs
/*
   +-----+---+---+---------------------+--------+--------+---------+
   | No. | C | R | Name                | Format | Length | Default |
   +-----+---+---+---------------------+--------+--------+---------+
   |   2 |   |   | Bad-CSM-Option      | uint   | 0-2    | (none)  |
   +-----+---+---+---------------------+--------+--------+---------+
   C=Critical, R=Repeatable
*/
const (
	BadCSMOption OptionID = 2
)

var signalCSMOptionDefs = map[OptionID]optionDef{
	MaxMessageSize:    optionDef{valueFormat: valueUint, minLen: 0, maxLen: 4},
	BlockWiseTransfer: optionDef{valueFormat: valueEmpty, minLen: 0, maxLen: 0},
}

var signalPingPongOptionDefs = map[OptionID]optionDef{
	Custody: optionDef{valueFormat: valueEmpty, minLen: 0, maxLen: 0},
}

var signalReleaseOptionDefs = map[OptionID]optionDef{
	AlternativeAddress: optionDef{valueFormat: valueString, minLen: 1, maxLen: 255},
	HoldOff:            optionDef{valueFormat: valueUint, minLen: 0, maxLen: 3},
}

var signalAbortOptionDefs = map[OptionID]optionDef{
	BadCSMOption: optionDef{valueFormat: valueUint, minLen: 0, maxLen: 2},
}

// TcpMessage is a CoAP MessageBase that can encode itself for TCP
// transport.
type TCPMessage struct {
	Code COAPCode

	Token   []byte
	Payload []byte

	Uint32Options Uint32Options //Uint32Options must be sorted by ID
	StringOptions StringOptions //StringOptions must be sorted by ID
	BytesOptions  BytesOptions  //BytesOptions must be sorted by ID
}

type UseOptionsSlice uint8

const (
	UseOptionsNone   = UseOptionsSlice(0)
	UseUint32Options = UseOptionsSlice(1)
	UseStringOptions = UseOptionsSlice(2)
	UseBytesOptions  = UseOptionsSlice(3)
)

func (m TCPMessage) MarshalOptions(buf []byte) (int, ErrorCode) {
	previousID := OptionID(0)
	length := 0

	size := len(m.Uint32Options) + len(m.StringOptions) + len(m.BytesOptions)
	idxUint32 := 0
	idxString := 0
	idxBytes := 0
	for i := 0; i < size; i++ {
		minID := OptionID(255)
		use := UseOptionsNone
		if idxUint32 < len(m.Uint32Options) {
			minID = m.Uint32Options[idxUint32].ID
			use = UseUint32Options
		}
		if idxString < len(m.StringOptions) && m.StringOptions[idxString].ID < minID {
			minID = m.StringOptions[idxString].ID
			use = UseStringOptions
		}
		if idxBytes < len(m.BytesOptions) && m.BytesOptions[idxBytes].ID < minID {
			use = UseBytesOptions
		}

		//return ErrorCode but calculate length
		if length > len(buf) {
			buf = nil
		}

		var optionLength int
		var err ErrorCode
		switch use {
		case UseOptionsNone:
			panic("bug in library")
		case UseUint32Options:
			if buf != nil {
				optionLength, err = m.Uint32Options[idxUint32].Marshal(buf[length:], previousID)
			} else {
				optionLength, err = m.Uint32Options[idxUint32].Marshal(nil, previousID)
			}
			previousID = m.Uint32Options[idxUint32].ID
			idxUint32++
		case UseStringOptions:
			if buf != nil {
				optionLength, err = m.StringOptions[idxString].Marshal(buf[length:], previousID)
			} else {
				optionLength, err = m.StringOptions[idxString].Marshal(nil, previousID)
			}
			previousID = m.StringOptions[idxString].ID
			idxString++
		case UseBytesOptions:
			if buf != nil {
				optionLength, err = m.BytesOptions[idxBytes].Marshal(buf[length:], previousID)
			} else {
				optionLength, err = m.BytesOptions[idxBytes].Marshal(nil, previousID)
			}
			previousID = m.BytesOptions[idxBytes].ID
			idxBytes++
		}

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

func (m TCPMessage) Marshal(buf []byte) (int, ErrorCode) {
	/*
	   A CoAP TCP message looks like:

	        0                   1                   2                   3
	       0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	      +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	      |  Len  |  TKL  | Extended Length ...
	      +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	      |      Code     | TKL bytes ...
	      +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	      |   Options (if any) ...
	      +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	      |1 1 1 1 1 1 1 1|    Payload (if any) ...
	      +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

	   The size of the Extended Length field is inferred from the value of the
	   Len field as follows:

	   | Len value  | Extended Length size  | Total length              |
	   +------------+-----------------------+---------------------------+
	   | 0-12       | 0                     | Len                       |
	   | 13         | 1                     | Extended Length + 13      |
	   | 14         | 2                     | Extended Length + 269     |
	   | 15         | 4                     | Extended Length + 65805   |
	*/

	if len(m.Token) > MaxTokenSize {
		return -1, ErrorCodeInvalidTokenLen
	}

	payloadLen := len(m.Payload)
	if payloadLen > 0 {
		//for separator 0xff
		payloadLen++
	}
	optionsLen, err := m.MarshalOptions(nil)
	if err != ErrorCodeTooSmall {
		return -1, err
	}
	bufLen := payloadLen + optionsLen
	var lenNib uint8
	var extLenBytes []byte

	if bufLen < TCP_MESSAGE_LEN13_BASE {
		lenNib = uint8(bufLen)
	} else if bufLen < TCP_MESSAGE_LEN14_BASE {
		lenNib = 13
		extLen := bufLen - TCP_MESSAGE_LEN13_BASE
		extLenBytes = []byte{uint8(extLen)}
	} else if bufLen < TCP_MESSAGE_LEN15_BASE {
		lenNib = 14
		extLen := bufLen - TCP_MESSAGE_LEN14_BASE
		extLenBytes = make([]byte, 2)
		binary.BigEndian.PutUint16(extLenBytes, uint16(extLen))
	} else if bufLen < TCP_MESSAGE_MAX_LEN {
		lenNib = 15
		extLen := bufLen - TCP_MESSAGE_LEN15_BASE
		extLenBytes = make([]byte, 4)
		binary.BigEndian.PutUint32(extLenBytes, uint32(extLen))
	}

	var hdr [1 + 4 + MaxTokenSize + 1]byte
	hdrLen := 1 + len(extLenBytes) + len(m.Token) + 1
	hdrOff := 0

	// Length and TKL nibbles.
	hdr[hdrOff] = uint8(0xf&len(m.Token)) | (lenNib << 4)
	hdrOff++

	// Extended length, if present.
	if len(extLenBytes) > 0 {
		copy(hdr[hdrOff:hdrOff+len(extLenBytes)], extLenBytes)
		hdrOff += len(extLenBytes)
	}

	// Code.
	hdr[hdrOff] = byte(m.Code)
	hdrOff++

	// Token.
	if len(m.Token) > 0 {
		copy(hdr[hdrOff:hdrOff+len(m.Token)], m.Token)
		hdrOff += len(m.Token)
	}

	bufLen = bufLen + hdrLen
	if len(buf) < bufLen {
		return bufLen, ErrorCodeTooSmall
	}

	copy(buf, hdr[:hdrLen])
	optionsLen, err = m.MarshalOptions(buf[hdrLen:])
	switch err {
	case OK:
	case ErrorCodeTooSmall:
		return bufLen, err
	default:
		return -1, err
	}
	if len(m.Payload) > 0 {
		copy(buf[hdrLen+optionsLen:], []byte{0xff})
		copy(buf[hdrLen+optionsLen+1:], m.Payload)
	}

	return bufLen, OK
}

type TCPMessageHeader struct {
	token  []byte
	code   COAPCode
	hdrLen int
	totLen int
}

// Unmarshal infers information about a TCP CoAP message from the first
// fragment.
func (i *TCPMessageHeader) Unmarshal(data []byte) (int, ErrorCode) {
	hdrOff := 0
	if len(data) == 0 {
		return 0, ErrorCodeShortRead
	}

	firstByte := data[0]
	data = data[1:]
	hdrOff++

	lenNib := (firstByte & 0xf0) >> 4
	tkl := firstByte & 0x0f

	var opLen int
	switch {
	case lenNib < TCP_MESSAGE_LEN13_BASE:
		opLen = int(lenNib)
	case lenNib == 13:
		if len(data) < 1 {
			return 0, ErrorCodeShortRead
		}
		extLen := data[0]
		data = data[1:]
		hdrOff++
		opLen = TCP_MESSAGE_LEN13_BASE + int(extLen)
	case lenNib == 14:
		if len(data) < 2 {
			return 0, ErrorCodeShortRead
		}
		extLen := binary.BigEndian.Uint16(data)
		data = data[2:]
		hdrOff += 2
		opLen = TCP_MESSAGE_LEN14_BASE + int(extLen)
	case lenNib == 15:
		if len(data) < 4 {
			return 0, ErrorCodeShortRead
		}
		extLen := binary.BigEndian.Uint32(data)
		data = data[4:]
		hdrOff += 4
		opLen = TCP_MESSAGE_LEN15_BASE + int(extLen)
	}

	i.totLen = hdrOff + 1 + int(tkl) + opLen
	if len(data) < 1 {
		return 0, ErrorCodeShortRead
	}
	i.code = COAPCode(data[0])
	data = data[1:]
	hdrOff++
	if len(i.token) < int(tkl) {
		return 0, ErrorCodeInvalidTokenLen
	}
	if len(data) < int(tkl) {
		return 0, ErrorCodeShortRead
	}
	copy(i.token, data[:tkl])
	hdrOff += int(tkl)

	i.hdrLen = hdrOff

	return i.hdrLen, OK
}

func parseExtOpt(data []byte, opt int) (int, int, ErrorCode) {
	processed := 0
	switch opt {
	case extoptByteCode:
		if len(data) < 1 {
			return 0, -1, ErrorCodeOptionTruncated
		}
		opt = int(data[0]) + extoptByteAddend
		processed = 1
	case extoptWordCode:
		if len(data) < 2 {
			return 0, -1, ErrorCodeOptionTruncated
		}
		opt = int(binary.BigEndian.Uint16(data[:2])) + extoptWordAddend
		processed = 2
	}
	return processed, opt, OK
}

func (m *TCPMessage) UnmarshalOption(data []byte, optionDefs map[OptionID]optionDef, optionID OptionID) (int, ErrorCode) {
	if def, ok := optionDefs[optionID]; ok {
		if def.valueFormat == valueUnknown {
			// Skip unrecognized options (RFC7252 section 5.4.1)
			return 0, OK
		}
		if len(data) < def.minLen || len(data) > def.maxLen {
			// Skip options with illegal value length (RFC7252 section 5.4.3)
			return 0, OK
		}
		switch def.valueFormat {
		case valueUint:
			if cap(m.Uint32Options) == len(m.Uint32Options) {
				return -1, ErrorCodeUint32OptionsTooSmall
			}
			o := Uint32Option{ID: optionID}
			proc, err := o.UnmarshalValue(data)
			if err != OK {
				return -1, err
			}
			m.Uint32Options = m.Uint32Options[:len(m.Uint32Options)+1]
			m.Uint32Options[len(m.Uint32Options)-1] = o
			return proc, err
		case valueString:
			if cap(m.StringOptions) == len(m.StringOptions) {
				return -1, ErrorCodeStringOptionsTooSmall
			}
			o := StringOption{ID: optionID}
			proc, err := o.UnmarshalValue(data)
			if err != OK {
				return -1, err
			}
			m.StringOptions = m.StringOptions[:len(m.StringOptions)+1]
			m.StringOptions[len(m.StringOptions)-1] = o
			return proc, err
		case valueOpaque, valueEmpty:
			if cap(m.BytesOptions) == len(m.BytesOptions) {
				return -1, ErrorCodeBytesOptionsTooSmall
			}
			o := BytesOption{ID: optionID}
			proc, err := o.UnmarshalValue(data)
			if err != OK {
				return -1, err
			}
			m.BytesOptions = m.BytesOptions[:len(m.BytesOptions)+1]
			m.BytesOptions[len(m.BytesOptions)-1] = o
			return proc, err
		}
	}
	// Skip unrecognized options (should never be reached)
	return 0, OK
}

func (m *TCPMessage) UnmarshalOptions(data []byte, optionDefs map[OptionID]optionDef) (int, ErrorCode) {
	prev := 0
	processed := 0

	for len(data) > 0 {
		if data[0] == 0xff {
			processed++
			break
		}

		delta := int(data[0] >> 4)
		length := int(data[0] & 0x0f)

		if delta == extoptError || length == extoptError {
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

		oid := OptionID(prev + delta)
		proc, err = m.UnmarshalOption(data[:length], optionDefs, oid)
		if err != OK {
			return -1, err
		}
		processed += proc
		data = data[proc:]
		prev = int(oid)
	}

	return processed, OK
}

func (m *TCPMessage) Unmarshal(data []byte) (int, ErrorCode) {
	header := TCPMessageHeader{token: m.Token}
	processed, err := header.Unmarshal(data)
	if err != OK {
		return -1, err
	}
	if len(data) < header.totLen {
		return -1, ErrorCodeShortRead
	}
	data = data[processed:]

	optionDefs := coapOptionDefs
	switch COAPCode(header.code) {
	case CSM:
		optionDefs = signalCSMOptionDefs
	case Ping, Pong:
		optionDefs = signalPingPongOptionDefs
	case Release:
		optionDefs = signalReleaseOptionDefs
	case Abort:
		optionDefs = signalAbortOptionDefs
	}

	proc, err := m.UnmarshalOptions(data, optionDefs)
	if err != OK {
		return -1, err
	}
	data = data[proc:]
	processed += proc

	m.Payload = data
	processed = processed + len(data)
	m.Code = header.code
	m.Token = header.token

	return 0, OK
}
