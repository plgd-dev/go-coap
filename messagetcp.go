package coap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

const (
	TCP_MESSAGE_LEN13_BASE = 13
	TCP_MESSAGE_LEN14_BASE = 269
	TCP_MESSAGE_LEN15_BASE = 65805
	TCP_MESSAGE_MAX_LEN    = 0x7fff0000 // Large number that works in 32-bit builds.
)

// TcpMessage is a CoAP MessageBase that can encode itself for TCP
// transport.
type TcpMessage struct {
	MessageBase
}

func NewTcpMessage(p MessageParams) *TcpMessage {
	return &TcpMessage{
		MessageBase{
			typ:       p.Type,
			code:      p.Code,
			messageID: p.MessageID,
			token:     p.Token,
			payload:   p.Payload,
		},
	}
}

func (m *TcpMessage) MarshalBinary() ([]byte, error) {
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

	if len(m.MessageBase.token) > 8 {
		return nil, ErrInvalidTokenLen
	}

	buf := bytes.Buffer{}

	sort.Stable(&m.MessageBase.opts)
	writeOpts(&buf, m.MessageBase.opts)

	if len(m.MessageBase.payload) > 0 {
		buf.Write([]byte{0xff})
		buf.Write(m.MessageBase.payload)
	}

	var lenNib uint8
	var extLenBytes []byte

	if buf.Len() < TCP_MESSAGE_LEN13_BASE {
		lenNib = uint8(buf.Len())
	} else if buf.Len() < TCP_MESSAGE_LEN14_BASE {
		lenNib = 13
		extLen := buf.Len() - TCP_MESSAGE_LEN13_BASE
		extLenBytes = []byte{uint8(extLen)}
	} else if buf.Len() < TCP_MESSAGE_LEN15_BASE {
		lenNib = 14
		extLen := buf.Len() - TCP_MESSAGE_LEN14_BASE
		extLenBytes = make([]byte, 2)
		binary.BigEndian.PutUint16(extLenBytes, uint16(extLen))
	} else if buf.Len() < TCP_MESSAGE_MAX_LEN {
		lenNib = 15
		extLen := buf.Len() - TCP_MESSAGE_LEN15_BASE
		extLenBytes = make([]byte, 4)
		binary.BigEndian.PutUint32(extLenBytes, uint32(extLen))
	}

	hdr := make([]byte, 1+len(extLenBytes)+len(m.MessageBase.token)+1)
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

	return append(hdr, buf.Bytes()...), nil
}

// msgTcpInfo describes a single TCP CoAP message.  Used during reassembly.
type msgTcpInfo struct {
	typ    uint8
	token  []byte
	code   uint8
	hdrLen int
	totLen int
}

func normalizeErrors(e error) error {
	if e == io.EOF || e == io.ErrUnexpectedEOF {
		return io.ErrUnexpectedEOF
	}
	return e
}

// readTcpMsgInfo infers information about a TCP CoAP message from the first
// fragment.
func readTcpMsgInfo(r io.Reader) (msgTcpInfo, error) {
	mti := msgTcpInfo{}

	hdrOff := 0

	var firstByte byte
	if err := binary.Read(r, binary.BigEndian, &firstByte); err != nil {
		return mti, normalizeErrors(err)
	}
	hdrOff++

	lenNib := (firstByte & 0xf0) >> 4
	tkl := firstByte & 0x0f

	var opLen int
	switch {
	case lenNib < TCP_MESSAGE_LEN13_BASE:
		opLen = int(lenNib)
	case lenNib == 13:
		var extLen byte
		if err := binary.Read(r, binary.BigEndian, &extLen); err != nil {
			return mti, err
		}
		hdrOff++
		opLen = TCP_MESSAGE_LEN13_BASE + int(extLen)
	case lenNib == 14:
		var extLen uint16
		if err := binary.Read(r, binary.BigEndian, &extLen); err != nil {
			return mti, err
		}
		hdrOff += 2
		opLen = TCP_MESSAGE_LEN14_BASE + int(extLen)
	case lenNib == 15:
		var extLen uint32
		if err := binary.Read(r, binary.BigEndian, &extLen); err != nil {
			return mti, err
		}
		hdrOff += 4
		opLen = TCP_MESSAGE_LEN15_BASE + int(extLen)
	}

	mti.totLen = hdrOff + 1 + int(tkl) + opLen

	if err := binary.Read(r, binary.BigEndian, &mti.code); err != nil {
		return mti, err
	}
	hdrOff++

	mti.token = make([]byte, tkl)
	if _, err := io.ReadFull(r, mti.token); err != nil {
		return mti, err
	}
	hdrOff += int(tkl)

	mti.hdrLen = hdrOff

	return mti, nil
}

func readTcpMsgBody(mti msgTcpInfo, r io.Reader) (options, []byte, error) {
	bodyLen := mti.totLen - mti.hdrLen
	b := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, nil, err
	}

	o, p, err := parseBody(b)
	if err != nil {
		return nil, nil, err
	}

	return o, p, nil
}

func (m *TcpMessage) fill(mti msgTcpInfo, o options, p []byte) {
	m.MessageBase.typ = COAPType(mti.typ)
	m.MessageBase.code = COAPCode(mti.code)
	m.MessageBase.token = mti.token
	m.MessageBase.opts = o
	m.MessageBase.payload = p
}

func (m *TcpMessage) UnmarshalBinary(data []byte) error {
	r := bytes.NewReader(data)

	mti, err := readTcpMsgInfo(r)
	if err != nil {
		return fmt.Errorf("Error reading TCP CoAP header; %s", err.Error())
	}

	if len(data) != mti.totLen {
		return fmt.Errorf("CoAP length mismatch (hdr=%d pkt=%d)",
			mti.totLen, len(data))
	}

	o, p, err := readTcpMsgBody(mti, r)
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
func Decode(r io.Reader) (*TcpMessage, error) {
	mti, err := readTcpMsgInfo(r)
	if err != nil {
		return nil, err
	}

	o, p, err := readTcpMsgBody(mti, r)
	if err != nil {
		return nil, err
	}

	m := &TcpMessage{}
	m.fill(mti, o, p)

	return m, nil
}
