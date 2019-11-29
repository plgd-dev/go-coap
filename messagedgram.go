package coap

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"

	"github.com/go-ocf/go-coap/codes"
)

// DgramMessage implements Message interface.
type DgramMessage struct {
	MessageBase
	messageID uint16
}

func NewDgramMessage(p MessageParams) *DgramMessage {
	return &DgramMessage{
		MessageBase: MessageBase{
			typ:     p.Type,
			code:    p.Code,
			token:   p.Token,
			payload: p.Payload,
		},
		messageID: p.MessageID,
	}
}

func (m *DgramMessage) MessageID() uint16 {
	return m.messageID
}

// SetMessageID
func (m *DgramMessage) SetMessageID(messageID uint16) {
	m.messageID = messageID
}

// MarshalBinary produces the binary form of this DgramMessage.
func (m *DgramMessage) MarshalBinary(buf io.Writer) error {
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

	buf.Write([]byte{
		(1 << 6) | (uint8(m.Type()) << 4) | uint8(0xf&len(m.MessageBase.token)),
		byte(m.MessageBase.code),
		tmpbuf[0], tmpbuf[1],
	})

	if len(m.MessageBase.token) > MaxTokenSize {
		return ErrInvalidTokenLen
	}
	buf.Write(m.MessageBase.token)

	sort.Stable(&m.MessageBase.opts)
	writeOpts(buf, m.MessageBase.opts)

	if len(m.MessageBase.payload) > 0 {
		buf.Write([]byte{0xff})
	}

	buf.Write(m.MessageBase.payload)

	return nil
}

// UnmarshalBinary parses the given binary slice as a DgramMessage.
func (m *DgramMessage) UnmarshalBinary(data []byte) error {
	if len(data) < 4 {
		return ErrMessageTruncated
	}

	if data[0]>>6 != 1 {
		return ErrMessageInvalidVersion
	}

	m.MessageBase.typ = COAPType((data[0] >> 4) & 0x3)
	tokenLen := int(data[0] & 0xf)
	if tokenLen > 8 {
		return ErrInvalidTokenLen
	}

	m.MessageBase.code = codes.Code(data[1])
	m.messageID = binary.BigEndian.Uint16(data[2:4])

	if tokenLen > 0 {
		m.MessageBase.token = make([]byte, tokenLen)
	}
	if len(data) < 4+tokenLen {
		return ErrMessageTruncated
	}
	copy(m.MessageBase.token, data[4:4+tokenLen])
	b := data[4+tokenLen:]

	o, p, err := parseBody(coapOptionDefs, b)
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

// ToBytesLength gets the length of the message
func (m *DgramMessage) ToBytesLength() (int, error) {
	buf := bytes.NewBuffer(make([]byte, 0, 1024))
	if err := m.MarshalBinary(buf); err != nil {
		return 0, err
	}

	return len(buf.Bytes()), nil
}
