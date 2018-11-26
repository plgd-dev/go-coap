package messagetcp

import (
	"encoding/binary"

	coap "github.com/go-ocf/go-coap/g2/message"
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
	MaxMessageSize    coap.OptionID = 2
	BlockWiseTransfer coap.OptionID = 4
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
	Custody coap.OptionID = 2
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
	AlternativeAddress coap.OptionID = 2
	HoldOff            coap.OptionID = 4
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
	BadCSMOption coap.OptionID = 2
)

var signalCSMOptionDefs = map[coap.OptionID]coap.OptionDef{
	MaxMessageSize:    coap.OptionDef{ValueFormat: coap.ValueUint, MinLen: 0, MaxLen: 4},
	BlockWiseTransfer: coap.OptionDef{ValueFormat: coap.ValueEmpty, MinLen: 0, MaxLen: 0},
}

var signalPingPongOptionDefs = map[coap.OptionID]coap.OptionDef{
	Custody: coap.OptionDef{ValueFormat: coap.ValueEmpty, MinLen: 0, MaxLen: 0},
}

var signalReleaseOptionDefs = map[coap.OptionID]coap.OptionDef{
	AlternativeAddress: coap.OptionDef{ValueFormat: coap.ValueString, MinLen: 1, MaxLen: 255},
	HoldOff:            coap.OptionDef{ValueFormat: coap.ValueUint, MinLen: 0, MaxLen: 3},
}

var signalAbortOptionDefs = map[coap.OptionID]coap.OptionDef{
	BadCSMOption: coap.OptionDef{ValueFormat: coap.ValueUint, MinLen: 0, MaxLen: 2},
}

// TcpMessage is a CoAP MessageBase that can encode itself for TCP
// transport.
type TCP struct {
	Code coap.COAPCode

	Token   []byte
	Payload []byte

	Options coap.Options //Options must be sorted by ID
}

func (m TCP) Marshal(buf []byte) (int, coap.ErrorCode) {
	/*
	   A CoAP TCP message locoap.OKs like:

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

	if len(m.Token) > coap.MaxTokenSize {
		return -1, coap.ErrorCodeInvalidTokenLen
	}

	payloadLen := len(m.Payload)
	if payloadLen > 0 {
		//for separator 0xff
		payloadLen++
	}
	optionsLen, err := m.Options.Marshal(nil)
	if err != coap.ErrorCodeTooSmall {
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

	var hdr [1 + 4 + coap.MaxTokenSize + 1]byte
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
		return bufLen, coap.ErrorCodeTooSmall
	}

	copy(buf, hdr[:hdrLen])
	optionsLen, err = m.Options.Marshal(buf[hdrLen:])
	switch err {
	case coap.OK:
	case coap.ErrorCodeTooSmall:
		return bufLen, err
	default:
		return -1, err
	}
	if len(m.Payload) > 0 {
		copy(buf[hdrLen+optionsLen:], []byte{0xff})
		copy(buf[hdrLen+optionsLen+1:], m.Payload)
	}

	return bufLen, coap.OK
}

type TCPHeader struct {
	Token  []byte
	code   coap.COAPCode
	hdrLen int
	totLen int
}

// Unmarshal infers information about a TCP CoAP message from the first
// fragment.
func (i *TCPHeader) Unmarshal(data []byte) (int, coap.ErrorCode) {
	hdrOff := 0
	if len(data) == 0 {
		return 0, coap.ErrorCodeShortRead
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
			return 0, coap.ErrorCodeShortRead
		}
		extLen := data[0]
		data = data[1:]
		hdrOff++
		opLen = TCP_MESSAGE_LEN13_BASE + int(extLen)
	case lenNib == 14:
		if len(data) < 2 {
			return 0, coap.ErrorCodeShortRead
		}
		extLen := binary.BigEndian.Uint16(data)
		data = data[2:]
		hdrOff += 2
		opLen = TCP_MESSAGE_LEN14_BASE + int(extLen)
	case lenNib == 15:
		if len(data) < 4 {
			return 0, coap.ErrorCodeShortRead
		}
		extLen := binary.BigEndian.Uint32(data)
		data = data[4:]
		hdrOff += 4
		opLen = TCP_MESSAGE_LEN15_BASE + int(extLen)
	}

	i.totLen = hdrOff + 1 + int(tkl) + opLen
	if len(data) < 1 {
		return 0, coap.ErrorCodeShortRead
	}
	i.code = coap.COAPCode(data[0])
	data = data[1:]
	hdrOff++
	if len(data) < int(tkl) {
		return 0, coap.ErrorCodeShortRead
	}
	i.Token = data[:tkl]
	hdrOff += int(tkl)

	i.hdrLen = hdrOff

	return i.hdrLen, coap.OK
}

func (m *TCP) Unmarshal(data []byte) (int, coap.ErrorCode) {
	header := TCPHeader{Token: m.Token}
	processed, err := header.Unmarshal(data)
	if err != coap.OK {
		return -1, err
	}
	if len(data) < header.totLen {
		return -1, coap.ErrorCodeShortRead
	}
	data = data[processed:]

	optionDefs := coap.CoapOptionDefs
	switch coap.COAPCode(header.code) {
	case coap.CSM:
		optionDefs = signalCSMOptionDefs
	case coap.Ping, coap.Pong:
		optionDefs = signalPingPongOptionDefs
	case coap.Release:
		optionDefs = signalReleaseOptionDefs
	case coap.Abort:
		optionDefs = signalAbortOptionDefs
	}

	proc, err := m.Options.Unmarshal(data, optionDefs)
	if err != coap.OK {
		return -1, err
	}
	data = data[proc:]
	processed += proc

	m.Payload = data
	processed = processed + len(data)
	m.Code = header.code
	m.Token = header.Token

	return processed, coap.OK
}
