package coap

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/go-ocf/go-coap/codes"
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
type TcpMessage struct {
	MessageBase
}

func NewTcpMessage(p MessageParams) *TcpMessage {
	return &TcpMessage{
		MessageBase{
			//typ:       p.Type, not used by COAP over TCP
			code: p.Code,
			//messageID: p.MessageID,  not used by COAP over TCP
			token:   p.Token,
			payload: p.Payload,
		},
	}
}

func (m *TcpMessage) MessageID() uint16 {
	return 0
}

// SetMessageID
func (m *TcpMessage) SetMessageID(messageID uint16) {
	//not used by COAP over TCP
}

func (m *TcpMessage) MarshalBinary(buf io.Writer) error {
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

	if len(m.MessageBase.token) > MaxTokenSize {
		return ErrInvalidTokenLen
	}

	sort.Stable(&m.MessageBase.opts)
	payloadLen := len(m.MessageBase.payload)
	if payloadLen > 0 {
		//for separator 0xff
		payloadLen++
	}
	optionsLen := bytesLengthOpts(m.MessageBase.opts)
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

	//hdr := make([]byte, 1+len(extLenBytes)+len(m.MessageBase.token)+1)
	var hdr [1 + 4 + MaxTokenSize + 1]byte
	hdrLen := 1 + len(extLenBytes) + len(m.MessageBase.token) + 1
	hdrOff := 0

	// Length and TKL nibbles.
	hdr[hdrOff] = uint8(0xf&len(m.MessageBase.token)) | (lenNib << 4)
	hdrOff++

	// Extended length, if present.
	if len(extLenBytes) > 0 {
		copy(hdr[hdrOff:hdrOff+len(extLenBytes)], extLenBytes)
		hdrOff += len(extLenBytes)
	}

	// Code.
	hdr[hdrOff] = byte(m.MessageBase.code)
	hdrOff++

	// Token.
	if len(m.MessageBase.token) > 0 {
		copy(hdr[hdrOff:hdrOff+len(m.MessageBase.token)], m.MessageBase.token)
		hdrOff += len(m.MessageBase.token)
	}

	bufLen = bufLen + len(hdr)
	buf.Write(hdr[:hdrLen])
	writeOpts(buf, m.MessageBase.opts)
	if len(m.MessageBase.payload) > 0 {
		buf.Write([]byte{0xff})
		buf.Write(m.MessageBase.payload)
	}

	return nil
}

// msgTcpInfo describes a single TCP CoAP message.  Used during reassembly.
type msgTcpInfo struct {
	typ    uint8
	token  []byte
	code   uint8
	hdrLen int
	totLen int
}

func (m msgTcpInfo) BodyLen() int {
	return m.totLen - m.hdrLen
}

func normalizeErrors(e error) error {
	if e == io.EOF || e == io.ErrUnexpectedEOF {
		return io.ErrUnexpectedEOF
	}
	return e
}

// readTcpMsgInfo infers information about a TCP CoAP message from the first
// fragment.

type contextReader interface {
	ReadFullWithContext(context.Context, []byte) error
}

func readTcpMsgInfo(ctx context.Context, conn contextReader) (msgTcpInfo, error) {
	mti := msgTcpInfo{}

	hdrOff := 0

	firstByte := make([]byte, 1)
	err := conn.ReadFullWithContext(ctx, firstByte)
	if err != nil {
		return mti, fmt.Errorf("cannot read coap header: %v", err)
	}
	hdrOff++

	lenNib := (firstByte[0] & 0xf0) >> 4
	tkl := firstByte[0] & 0x0f

	var opLen int
	switch {
	case lenNib < TCP_MESSAGE_LEN13_BASE:
		opLen = int(lenNib)
	case lenNib == 13:
		extLen := make([]byte, 1)
		err := conn.ReadFullWithContext(ctx, extLen)
		if err != nil {
			return mti, fmt.Errorf("cannot read coap header: %v", err)
		}
		hdrOff++
		opLen = TCP_MESSAGE_LEN13_BASE + int(extLen[0])
	case lenNib == 14:
		extLen := make([]byte, 2)
		err := conn.ReadFullWithContext(ctx, extLen)
		if err != nil {
			return mti, fmt.Errorf("cannot read coap header: %v", err)
		}
		hdrOff += 2
		opLen = TCP_MESSAGE_LEN14_BASE + int(binary.BigEndian.Uint16(extLen))
	case lenNib == 15:
		extLen := make([]byte, 4)
		err := conn.ReadFullWithContext(ctx, extLen)
		if err != nil {
			return mti, fmt.Errorf("cannot read coap header: %v", err)
		}
		hdrOff += 4
		opLen = TCP_MESSAGE_LEN15_BASE + int(binary.BigEndian.Uint32(extLen))
	}

	mti.totLen = hdrOff + 1 + int(tkl) + opLen
	code := make([]byte, 1)
	err = conn.ReadFullWithContext(ctx, code)
	if err != nil {
		return mti, fmt.Errorf("cannot read coap header: %v", err)
	}
	mti.code = uint8(code[0])
	hdrOff++

	mti.token = make([]byte, tkl)
	if err := conn.ReadFullWithContext(ctx, mti.token); err != nil {
		return mti, err
	}
	hdrOff += int(tkl)

	mti.hdrLen = hdrOff

	return mti, nil
}

func parseTcpOptionsPayload(mti msgTcpInfo, b []byte) (options, []byte, error) {
	optionDefs := coapOptionDefs
	switch codes.Code(mti.code) {
	case codes.CSM:
		optionDefs = signalCSMOptionDefs
	case codes.Ping, codes.Pong:
		optionDefs = signalPingPongOptionDefs
	case codes.Release:
		optionDefs = signalReleaseOptionDefs
	case codes.Abort:
		optionDefs = signalAbortOptionDefs
	}

	o, p, err := parseBody(optionDefs, b)
	if err != nil {
		return nil, nil, err
	}

	return o, p, nil
}

func (m *TcpMessage) fill(mti msgTcpInfo, o options, p []byte) {
	m.MessageBase.typ = COAPType(mti.typ)
	m.MessageBase.code = codes.Code(mti.code)
	m.MessageBase.token = mti.token
	m.MessageBase.opts = o
	m.MessageBase.payload = p
}

func (m *TcpMessage) ToBytesLength() (int, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	if err := m.MarshalBinary(buf); err != nil {
		return 0, err
	}

	return len(buf.Bytes()), nil
}

type contextBytesReader struct {
	reader io.Reader
}

func (r *contextBytesReader) ReadFullWithContext(ctx context.Context, b []byte) error {
	_, err := io.ReadFull(r.reader, b)
	return err
}

func (m *TcpMessage) UnmarshalBinary(data []byte) error {

	r := &contextBytesReader{reader: bytes.NewReader(data)}

	mti, err := readTcpMsgInfo(context.Background(), r)
	if err != nil {
		return fmt.Errorf("Error reading TCP CoAP header; %s", err.Error())
	}

	if len(data) != mti.totLen {
		return fmt.Errorf("CoAP length mismatch (hdr=%d pkt=%d)",
			mti.totLen, len(data))
	}

	b := make([]byte, mti.BodyLen())
	err = r.ReadFullWithContext(context.Background(), b)
	if err != nil {
		return fmt.Errorf("cannot read TCP CoAP body: %v", err)
	}

	o, p, err := parseTcpOptionsPayload(mti, b)
	if err != nil {
		return err
	}

	m.fill(mti, o, p)
	return nil
}

// PullTcp extracts a complete TCP CoAP message from the front of a byte queue.
//
// Return values:
//  *TcpMessage: On success, points to the extracted message; nil if a complete
//               message could not be extracted.
//  []byte: The unread portion of of the supplied byte buffer.  If a message
//          was not extracted, this is the unchanged buffer that was passed in.
//  error: Non-nil if the buffer contains an invalid CoAP message.
//
// Note: It is not an error if the supplied buffer does not contain a complete
// message.  In such a case, nil *TclMessage and error values are returned
// along with the original buffer.
func PullTcp(data []byte) (*TcpMessage, []byte, error) {
	r := bytes.NewReader(data)
	m, err := Decode(r)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// Packet is incomplete.
			return nil, data, nil
		} else {
			// Some other error.
			return nil, data, err
		}
	}

	// Determine the number of bytes read.  These bytes get trimmed from the
	// front of the returned data slice.
	sz, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		// This should never happen.
		return nil, data, err
	}

	return m, data[sz:], nil
}

// Decode reads a single message from its input.
func Decode(reader io.Reader) (*TcpMessage, error) {
	r := &contextBytesReader{reader: reader}
	mti, err := readTcpMsgInfo(context.Background(), r)
	if err != nil {
		return nil, err
	}

	b := make([]byte, mti.BodyLen())
	err = r.ReadFullWithContext(context.Background(), b)
	if err != nil {
		return nil, fmt.Errorf("cannot read TCP CoAP body: %v", err)
	}

	o, p, err := parseTcpOptionsPayload(mti, b)
	if err != nil {
		return nil, err
	}

	m := &TcpMessage{}
	m.fill(mti, o, p)

	return m, nil
}
