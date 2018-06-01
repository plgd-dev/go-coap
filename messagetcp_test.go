package coap

import (
	"bytes"
	"testing"
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
	if msg.Code() != GET {
		t.Errorf("Expected message code GET, got %v", msg.Code())
	}

	if !bytes.Equal(msg.Payload(), []byte("hi")) {
		t.Errorf("Incorrect payload: %q", msg.Payload())
	}
}
