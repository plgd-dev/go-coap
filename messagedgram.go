package coap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sort"
)

// DgramMessage implements Message interface.
type DgramMessage struct {
	MessageBase
}

func NewDgramMessage(p MessageParams) *DgramMessage {
	return &DgramMessage{
		MessageBase{
			typ:       p.Type,
			code:      p.Code,
			messageID: p.MessageID,
			token:     p.Token,
			payload:   p.Payload,
		},
	}
}

// MarshalBinary produces the binary form of this DgramMessage.
func (m *DgramMessage) MarshalBinary() ([]byte, error) {
	tmpbuf := []byte{0, 0}
	binary.BigEndian.PutUint16(tmpbuf, m.MessageID())

	/*
	     0                   1                   2                   3
	    0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
	   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	   |Ver| T |  TKL  |      Code     |          Message ID           |
	   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	   |   Token (if any, TKL bytes) ...
	   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	   |   Options (if any) ...
	   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	   |1 1 1 1 1 1 1 1|    Payload (if any) ...
	   +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
	*/

	buf := bytes.Buffer{}
	buf.Write([]byte{
		(1 << 6) | (uint8(m.Type()) << 4) | uint8(0xf&len(m.MessageBase.token)),
		byte(m.MessageBase.code),
		tmpbuf[0], tmpbuf[1],
	})
	buf.Write(m.MessageBase.token)

	sort.Stable(&m.MessageBase.opts)
	writeOpts(&buf, m.MessageBase.opts)

	if len(m.MessageBase.payload) > 0 {
		buf.Write([]byte{0xff})
	}

	buf.Write(m.MessageBase.payload)

	return buf.Bytes(), nil
}

// UnmarshalBinary parses the given binary slice as a DgramMessage.
func (m *DgramMessage) UnmarshalBinary(data []byte) error {
	if len(data) < 4 {
		return errors.New("short packet")
	}

	if data[0]>>6 != 1 {
		return errors.New("invalid version")
	}

	m.MessageBase.typ = COAPType((data[0] >> 4) & 0x3)
	tokenLen := int(data[0] & 0xf)
	if tokenLen > 8 {
		return ErrInvalidTokenLen
	}

	m.MessageBase.code = COAPCode(data[1])
	m.MessageBase.messageID = binary.BigEndian.Uint16(data[2:4])

	if tokenLen > 0 {
		m.MessageBase.token = make([]byte, tokenLen)
	}
	if len(data) < 4+tokenLen {
		return errors.New("truncated")
	}
	copy(m.MessageBase.token, data[4:4+tokenLen])
	b := data[4+tokenLen:]

	o, p, err := parseBody(b)
	if err != nil {
		return err
	}

	m.MessageBase.payload = p
	m.MessageBase.opts = o

	return nil
}

// ParseDgramMessage extracts the Message from the given input.
func ParseDgramMessage(data []byte) (*DgramMessage, error) {
	rv := &DgramMessage{}
	return rv, rv.UnmarshalBinary(data)
}
