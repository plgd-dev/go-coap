package coder

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func testMarshalMessage(t *testing.T, msg message.Message, buf []byte, expectedOut []byte) {
	length, err := DefaultCoder.Encode(msg, buf)
	require.NoError(t, err)
	buf = buf[:length]
	require.Equal(t, expectedOut, buf)
}

func testUnmarshalMessage(t *testing.T, msg message.Message, buf []byte, expectedOut message.Message) {
	_, err := DefaultCoder.Decode(buf, &msg)
	require.NoError(t, err)
	require.Equal(t, expectedOut, msg)
}

func TestMarshalMessage(t *testing.T) {
	buf := make([]byte, 1024)
	testMarshalMessage(t, message.Message{}, buf, []byte{0, 0})
	testMarshalMessage(t, message.Message{Code: codes.GET}, buf, []byte{0, byte(codes.GET)})
	testMarshalMessage(t, message.Message{Code: codes.GET, Payload: []byte{0x1}}, buf, []byte{32, byte(codes.GET), 0xff, 0x1})
	testMarshalMessage(t, message.Message{Code: codes.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}}, buf, []byte{35, byte(codes.GET), 0x1, 0x2, 0x3, 0xff, 0x1})
	bufOptions := make([]byte, 1024)
	bufOptionsUsed := bufOptions
	options := make(message.Options, 0, 32)
	enc := 0
	options, enc, err := options.SetPath(bufOptionsUsed, "/a/b/c/d/e")
	if err != nil {
		t.Fatalf("Cannot set uri")
	}
	bufOptionsUsed = bufOptionsUsed[enc:]
	options, _, err = options.SetContentFormat(bufOptionsUsed, message.TextPlain)
	if err != nil {
		t.Fatalf("Cannot set content format")
	}

	testMarshalMessage(t, message.Message{
		Code:    codes.GET,
		Payload: []byte{0x1},
		Token:   []byte{0x1, 0x2, 0x3},
		Options: options,
	}, buf, []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1})
}

func TestUnmarshalMessage(t *testing.T) {
	testUnmarshalMessage(t, message.Message{}, []byte{0, 0}, message.Message{})
	testUnmarshalMessage(t, message.Message{}, []byte{0, byte(codes.GET)}, message.Message{Code: codes.GET})
	testUnmarshalMessage(t, message.Message{}, []byte{32, byte(codes.GET), 0xff, 0x1}, message.Message{Code: codes.GET, Payload: []byte{0x1}})
	testUnmarshalMessage(t, message.Message{}, []byte{35, byte(codes.GET), 0x1, 0x2, 0x3, 0xff, 0x1}, message.Message{Code: codes.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}})
	testUnmarshalMessage(t, message.Message{Options: make(message.Options, 0, 32)}, []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1}, message.Message{
		Code:    codes.GET,
		Payload: []byte{0x1},
		Token:   []byte{0x1, 0x2, 0x3},
		Options: []message.Option{{ID: 11, Value: []byte{97}}, {ID: 11, Value: []byte{98}}, {ID: 11, Value: []byte{99}}, {ID: 11, Value: []byte{100}}, {ID: 11, Value: []byte{101}}, {ID: 12, Value: []byte{}}},
	})
}

func BenchmarkMarshalMessage(b *testing.B) {
	options := make(message.Options, 0, 32)
	bufOptions := make([]byte, 1024)
	bufOptionsUsed := bufOptions

	enc := 0

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
	buffer := []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1}
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

func FuzzDecode(f *testing.F) {
	f.Add([]byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1})

	f.Fuzz(func(t *testing.T, input_data []byte) {
		msg := message.Message{Options: make(message.Options, 0, 32)}
		_, _ = DefaultCoder.Decode(input_data, &msg)
	})
}
