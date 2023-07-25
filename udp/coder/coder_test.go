package coder

import (
	"math"
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

	// validate messageID
	_, err := DefaultCoder.Encode(message.Message{MessageID: 0}, buf)
	require.NoError(t, err)
	_, err = DefaultCoder.Encode(message.Message{MessageID: -1}, buf)
	require.Error(t, err)
	_, err = DefaultCoder.Encode(message.Message{MessageID: math.MaxUint16}, buf)
	require.NoError(t, err)
	_, err = DefaultCoder.Encode(message.Message{MessageID: math.MaxUint16 + 1}, buf)
	require.Error(t, err)

	// validate type
	_, err = DefaultCoder.Encode(message.Message{Type: 0}, buf)
	require.NoError(t, err)
	_, err = DefaultCoder.Encode(message.Message{Type: -1}, buf)
	require.Error(t, err)
	_, err = DefaultCoder.Encode(message.Message{Type: math.MaxUint8}, buf)
	require.NoError(t, err)
	_, err = DefaultCoder.Encode(message.Message{Type: math.MaxUint8 + 1}, buf)
	require.Error(t, err)

	testMarshalMessage(t, message.Message{}, buf, []byte{64, 0, 0, 0})
	testMarshalMessage(t, message.Message{Code: codes.GET}, buf, []byte{64, byte(codes.GET), 0, 0})
	testMarshalMessage(t, message.Message{Code: codes.GET, Payload: []byte{0x1}}, buf, []byte{64, byte(codes.GET), 0, 0, 0xff, 0x1})
	testMarshalMessage(t, message.Message{Code: codes.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}}, buf, []byte{67, byte(codes.GET), 0, 0, 0x1, 0x2, 0x3, 0xff, 0x1})
	testMarshalMessage(t, message.Message{
		Code:      codes.BadRequest,
		Token:     []byte{0x86, 0xed, 0x9e, 0x84, 0x96, 0x13, 0x13, 0x9f},
		MessageID: 27562,
		Type:      message.NonConfirmable,
		Options: message.Options{
			{
				ID:    message.ETag,
				Value: []byte{0x14, 0xd2, 0xe, 0x17, 0xe7, 0xa0, 0xb7, 0x91},
			},
			{
				ID:    message.ContentFormat,
				Value: []byte{},
			},
			{
				ID:    message.Block2,
				Value: []byte{0x0e},
			},
			{
				ID:    message.Size2,
				Value: []byte{0x14, 0xd2},
			},
		},
	}, buf, []byte{88, 128, 107, 170, 134, 237, 158, 132, 150, 19, 19, 159, 72, 20, 210, 14, 23, 231, 160, 183, 145, 128, 177, 14, 82, 20, 210})

	bufOptions := make([]byte, 1024)
	bufOptionsUsed := bufOptions
	options := make(message.Options, 0, 32)
	enc := 0
	options, enc, err = options.SetPath(bufOptionsUsed, "/a/b/c/d/e")
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
	}, buf, []byte{67, 1, 0, 0, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1})

	testMarshalMessage(t, message.Message{}, buf, []byte{0x40, 0x0, 0x0, 0x0})
}

func TestUnmarshalMessage(t *testing.T) {
	testUnmarshalMessage(t, message.Message{Options: make(message.Options, 0, 32)}, []byte{88, 128, 107, 170, 134, 237, 158, 132, 150, 19, 19, 159, 72, 20, 210, 14, 23, 231, 160, 183, 145, 128, 177, 14, 82, 20, 210, 255}, message.Message{
		Code:      codes.BadRequest,
		Token:     []byte{0x86, 0xed, 0x9e, 0x84, 0x96, 0x13, 0x13, 0x9f},
		MessageID: 27562,
		Type:      message.NonConfirmable,
		Options: message.Options{
			{
				ID:    message.ETag,
				Value: []byte{0x14, 0xd2, 0xe, 0x17, 0xe7, 0xa0, 0xb7, 0x91},
			},
			{
				ID:    message.ContentFormat,
				Value: []byte{},
			},
			{
				ID:    message.Block2,
				Value: []byte{0x0e},
			},
			{
				ID:    message.Size2,
				Value: []byte{0x14, 0xd2},
			},
		},
	})

	testUnmarshalMessage(t, message.Message{Options: make(message.Options, 0, 32)}, []byte{0x48, 0x45, 0x62, 0xA2, 0x8D, 0xA2, 0x29, 0x0C, 0x18, 0x0F, 0xB5, 0x4A, 0x48, 0x5E, 0x10, 0xA3, 0x88, 0x00, 0x00, 0x00, 0x00, 0x82, 0x27, 0x10, 0x52, 0x27, 0x10, 0x61, 0x16, 0xE2, 0x06, 0xDD, 0x08, 0x00, 0x42, 0x08, 0x00, 0xFF, 0x70, 0x73, 0x9F, 0xBF, 0x62, 0x65, 0x70, 0x78, 0x1A, 0x63, 0x6F, 0x61, 0x70, 0x3A, 0x2F, 0x2F, 0x31, 0x30, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x30, 0x3A, 0x35, 0x37, 0x39, 0x34, 0x30, 0xFF, 0xBF, 0x62, 0x65, 0x70, 0x78, 0x1E, 0x63, 0x6F, 0x61, 0x70, 0x2B, 0x74, 0x63, 0x70, 0x3A, 0x2F, 0x2F, 0x31, 0x30, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x30, 0x3A, 0x34, 0x36, 0x33, 0x36, 0x33, 0xFF, 0xFF, 0xFF, 0xFF}, message.Message{
		Code:      codes.Content,
		Token:     []byte{0x8d, 0xa2, 0x29, 0x0c, 0x18, 0x0f, 0xb5, 0x4a},
		MessageID: 25250,
		Type:      message.Confirmable,
		Payload: []byte{
			0x70, 0x73, 0x9f, 0xbf, 0x62, 0x65, 0x70, 0x78, 0x1a, 0x63, 0x6f, 0x61, 0x70, 0x3a, 0x2f, 0x2f,
			0x31, 0x30, 0x2e, 0x31, 0x31, 0x32, 0x2e, 0x31, 0x31, 0x32, 0x2e, 0x31, 0x30, 0x3a, 0x35, 0x37,
			0x39, 0x34, 0x30, 0xff, 0xbf, 0x62, 0x65, 0x70, 0x78, 0x1e, 0x63, 0x6f, 0x61, 0x70, 0x2b, 0x74,
			0x63, 0x70, 0x3a, 0x2f, 0x2f, 0x31, 0x30, 0x2e, 0x31, 0x31, 0x32, 0x2e, 0x31, 0x31, 0x32, 0x2e,
			0x31, 0x30, 0x3a, 0x34, 0x36, 0x33, 0x36, 0x33, 0xff, 0xff, 0xff, 0xff,
		},
		Options: message.Options{
			{
				ID:    message.ETag,
				Value: []byte{0x5e, 0x10, 0xa3, 0x88, 0x00, 0x00, 0x00, 0x00},
			},
			{
				ID:    message.ContentFormat,
				Value: []byte{0x27, 0x10},
			},
			{
				ID:    message.Accept,
				Value: []byte{0x27, 0x10},
			},
			{
				ID:    message.Block2,
				Value: []byte{0x16},
			},
			{
				ID:    2049,
				Value: []byte{0x08, 0x00},
			},
			{
				ID:    2053,
				Value: []byte{0x08, 0x00},
			},
		},
	})
	testUnmarshalMessage(t, message.Message{Options: make(message.Options, 0, 32)}, []byte{0x48, 0x01, 0x00, 0x00, 0xB0, 0x35, 0x4C, 0xF5, 0xD9, 0x72, 0x24, 0x0D, 0x60, 0x55, 0x6C, 0x69, 0x67, 0x68, 0x74, 0x05, 0x6C, 0x69, 0x67, 0x68, 0x74}, message.Message{
		Code:  codes.GET,
		Token: []byte{0xb0, 0x35, 0x4c, 0xf5, 0xd9, 0x72, 0x24, 0x0d},
		Type:  message.Confirmable,
		Options: message.Options{
			{
				ID:    message.Observe,
				Value: []byte{},
			},
			{
				ID:    message.URIPath,
				Value: []byte{0x6c, 0x69, 0x67, 0x68, 0x74},
			},
			{
				ID:    message.URIPath,
				Value: []byte{0x6c, 0x69, 0x67, 0x68, 0x74},
			},
		},
	})
	testUnmarshalMessage(t, message.Message{}, []byte{64, 0, 0, 0}, message.Message{})
	testUnmarshalMessage(t, message.Message{}, []byte{64, byte(codes.GET), 0, 0}, message.Message{Code: codes.GET})
	testUnmarshalMessage(t, message.Message{}, []byte{64, byte(codes.GET), 0, 0, 0xff, 0x1}, message.Message{Code: codes.GET, Payload: []byte{0x1}})
	testUnmarshalMessage(t, message.Message{}, []byte{67, byte(codes.GET), 0, 0, 0x1, 0x2, 0x3, 0xff, 0x1}, message.Message{Code: codes.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}})
	testUnmarshalMessage(t, message.Message{Options: make(message.Options, 0, 32)}, []byte{67, 1, 0, 0, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1}, message.Message{
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

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x40, 0x1, 0x30, 0x39, 0x46, 0x77, 0x65, 0x65, 0x74, 0x61, 0x67, 0xa1, 0x3, 0xff, 'h', 'i'})
	f.Add([]byte{88, 128, 107, 170, 134, 237, 158, 132, 150, 19, 19, 159, 72, 20, 210, 14, 23, 231, 160, 183, 145, 128, 177, 14, 82, 20, 210, 255})
	f.Add([]byte{0x48, 0x45, 0x62, 0xA2, 0x8D, 0xA2, 0x29, 0x0C, 0x18, 0x0F, 0xB5, 0x4A, 0x48, 0x5E, 0x10, 0xA3, 0x88, 0x00, 0x00, 0x00, 0x00, 0x82, 0x27, 0x10, 0x52, 0x27, 0x10, 0x61, 0x16, 0xE2, 0x06, 0xDD, 0x08, 0x00, 0x42, 0x08, 0x00, 0xFF, 0x70, 0x73, 0x9F, 0xBF, 0x62, 0x65, 0x70, 0x78, 0x1A, 0x63, 0x6F, 0x61, 0x70, 0x3A, 0x2F, 0x2F, 0x31, 0x30, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x30, 0x3A, 0x35, 0x37, 0x39, 0x34, 0x30, 0xFF, 0xBF, 0x62, 0x65, 0x70, 0x78, 0x1E, 0x63, 0x6F, 0x61, 0x70, 0x2B, 0x74, 0x63, 0x70, 0x3A, 0x2F, 0x2F, 0x31, 0x30, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x31, 0x32, 0x2E, 0x31, 0x30, 0x3A, 0x34, 0x36, 0x33, 0x36, 0x33, 0xFF, 0xFF, 0xFF, 0xFF})

	f.Fuzz(func(t *testing.T, input_data []byte) {
		msg := message.Message{Options: make(message.Options, 0, 32)}
		_, _ = DefaultCoder.Decode(input_data, &msg)
	})
}
