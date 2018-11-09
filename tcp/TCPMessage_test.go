package tcpcoap

import (
	"bytes"
	"testing"

	coap "github.com/go-ocf/go-coap"
)

func testMarshalTCP(t *testing.T, msg TCPMessage, buf []byte, expectedOut []byte) {
	length, err := msg.Marshal(buf)
	if err != OK {
		t.Fatalf("Unexpected error: %d", err)
	}
	buf = buf[:length]
	if !bytes.Equal(buf, expectedOut) {
		t.Fatalf("Unexpected output %v, expeced %d", buf, expectedOut)
	}
}

func TestMarshalTCP(t *testing.T) {
	buf := make([]byte, 1024)
	testMarshalTCP(t, TCPMessage{}, buf, []byte{0, 0})
	testMarshalTCP(t, TCPMessage{Code: GET}, buf, []byte{0, byte(GET)})
	testMarshalTCP(t, TCPMessage{Code: GET, Payload: []byte{0x1}}, buf, []byte{32, byte(GET), 0xff, 0x1})
	testMarshalTCP(t, TCPMessage{Code: GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}}, buf, []byte{35, byte(GET), 0x1, 0x2, 0x3, 0xff, 0x1})
	stringOptions := make(StringOptions, 0, 32)
	stringOptions, _ = stringOptions.SetPath("/a/b/c/d/e")
	uint32Options := make(Uint32Options, 0, 32)
	uint32Options, _ = uint32Options.SetContentFormat(TextPlain)

	testMarshalTCP(t, TCPMessage{
		Code:          GET,
		Payload:       []byte{0x1},
		Token:         []byte{0x1, 0x2, 0x3},
		StringOptions: stringOptions,
		Uint32Options: uint32Options,
	}, buf, []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1})

	buffer := bytes.Buffer{}
	oldMsg := coap.NewTcpMessage(coap.MessageParams{Code: coap.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x4, 0x3}})
	oldMsg.SetPathString("/a/b/c/d/e")
	oldMsg.SetOption(coap.ContentFormat, coap.TextPlain)
	err := oldMsg.MarshalBinary(&buffer)
	if err != nil {
		t.Fatalf("Cannot marshal old tcpmessage %v", err)
	}

	testMarshalTCP(t, TCPMessage{
		Code:          GET,
		Payload:       []byte{0x1},
		Token:         []byte{0x1, 0x2, 0x3},
		StringOptions: stringOptions,
		Uint32Options: uint32Options,
	}, buf, buffer.Bytes())

}

func BenchmarkMarshalOldTCPMessage(b *testing.B) {
	buffer := bytes.NewBuffer(make([]byte, 0, 1024))
	msg := coap.NewTcpMessage(coap.MessageParams{Code: coap.GET, Payload: []byte{0x1}, Token: []byte{0x1, 0x2, 0x3}})
	msg.SetPathString("/a/b/c/d/e")
	msg.SetOption(coap.ContentFormat, coap.TextPlain)

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		err := msg.MarshalBinary(buffer)
		if err != nil {
			b.Fatalf("cannot marshal: %v", err)
		}
	}
}

func BenchmarkUnmarshalOldTCPMessage(b *testing.B) {
	buffer := []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1}
	msg := coap.TcpMessage{}

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		err := msg.UnmarshalBinary(buffer)
		if err != nil {
			b.Fatalf("cannot marshal: %v", err)
		}
	}
}

func BenchmarkMarshalTCPMessage(b *testing.B) {
	stringOptions := make(StringOptions, 0, 32)
	stringOptions, _ = stringOptions.SetPath("/a/b/c/d/e")
	uint32Options := make(Uint32Options, 0, 32)
	uint32Options, _ = uint32Options.SetContentFormat(TextPlain)

	msg := TCPMessage{
		Code:          GET,
		Payload:       []byte{0x1},
		Token:         []byte{0x1, 0x2, 0x3},
		StringOptions: stringOptions,
		Uint32Options: uint32Options,
	}
	buffer := make([]byte, 1024)

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		_, err := msg.Marshal(buffer)
		if err != OK {
			b.Fatalf("cannot marshal: %v", err)
		}
	}
}

func BenchmarkUnmarshalTCPMessage(b *testing.B) {
	buffer := []byte{211, 0, 1, 1, 2, 3, 177, 97, 1, 98, 1, 99, 1, 100, 1, 101, 16, 255, 1}
	stringOptions := make(StringOptions, 0, 32)
	uint32Options := make(Uint32Options, 0, 32)
	bytesOptions := make(BytesOptions, 0, 32)
	msg := TCPMessage{
		Token:         make([]byte, 8),
		StringOptions: stringOptions,
		Uint32Options: uint32Options,
		BytesOptions:  bytesOptions,
	}

	b.ResetTimer()
	for i := uint32(0); i < uint32(b.N); i++ {
		msg.StringOptions = stringOptions
		msg.Uint32Options = uint32Options
		_, err := msg.Unmarshal(buffer)
		if err != OK {
			b.Fatalf("cannot unmarshal: %v", err)
		}
	}
}
