package coap

import (
	"bytes"
	"testing"

	"github.com/go-ocf/go-coap/codes"
)

func TestTCPDecodeMessageSmallWithPayload(t *testing.T) {
	input := []byte{
		13 << 4, // len=13, tkl=0
		0x01,    // Extended Length
		0x01,    // Code
		0x30, 0x39, 0x21, 0x3,
		0x26, 0x77, 0x65, 0x65, 0x74, 0x61, 0x67,
		0xff,
		'h', 'i',
	}

	msg, err := Decode(bytes.NewReader(input))
	if err != nil {
		t.Fatalf("Error parsing message: %v", err)
	}

	if msg.Type() != Confirmable {
		t.Errorf("Expected message type confirmable, got %v", msg.Type())
	}
	if msg.Code() != codes.GET {
		t.Errorf("Expected message code GET, got %v", msg.Code())
	}

	if !bytes.Equal(msg.Payload(), []byte("hi")) {
		t.Errorf("Incorrect payload: %q", msg.Payload())
	}

}

func TestMessageTCPToBytesLength(t *testing.T) {
	msgParams := MessageParams{
		Code:    codes.Code(02),
		Token:   []byte{0xab},
		Payload: []byte("hi"),
	}

	msg := NewTcpMessage(msgParams)
	msg.AddOption(MaxMessageSize, maxMessageSize)

	buf := &bytes.Buffer{}
	err := msg.MarshalBinary(buf)
	if err != nil {
		t.Fatalf("Error encoding request: %v", err)
	}

	bytesLength, err := msg.ToBytesLength()
	if err != nil {
		t.Fatalf("Error parsing request: %v", err)
	}

	lenTkl := 1
	lenCode := 1
	maxMessageSizeOptionLength := 3
	payloadMarker := []byte{0xff}

	expectedLength := lenTkl + lenCode + len(msgParams.Token) + maxMessageSizeOptionLength + len(payloadMarker) + len(msgParams.Payload)
	if expectedLength != bytesLength {
		t.Errorf("Expected Length  = %d, got %d", expectedLength, bytesLength)
	}
}
