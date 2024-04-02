package coder

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func BenchmarkMarshalMessage(b *testing.B) {
	options := make(message.Options, 0, 32)
	bufOptions := make([]byte, 1024)
	bufOptionsUsed := bufOptions

	var enc int
	options, enc, _ = options.SetPath(bufOptionsUsed, "/a/b/c/d/e")
	bufOptionsUsed = bufOptionsUsed[enc:]

	options, _, _ = options.SetContentFormat(bufOptionsUsed, message.TextPlain)
	msg := message.Message{
		Code:    codes.GET,
		Payload: []byte{0x1},
		Token:   []byte{0x1, 0x2, 0x3},
		Options: options,
	}
	buffer := make([]byte, 1024)

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := DefaultCoder.Encode(msg, buffer)
		if err != nil {
			b.Fatalf("cannot marshal")
		}
	}
}

func BenchmarkUnmarshalMessage(b *testing.B) {
	buffer := []byte{
		0x40, 0x1, 0x30, 0x39, 0x46, 0x77,
		0x65, 0x65, 0x74, 0x61, 0x67, 0xa1, 0x3,
		0xff, 'h', 'i',
	}
	options := make(message.Options, 0, 32)
	msg := message.Message{
		Options: options,
	}

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		msg.Options = options
		_, err := DefaultCoder.Decode(buffer, &msg)
		if err != nil {
			b.Fatalf("cannot unmarshal: %v", err)
		}
	}
}
