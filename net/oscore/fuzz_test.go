package oscore

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func FuzzUnmarshalOption(f *testing.F) {
	f.Add([]byte{0x09, 0x14})
	f.Add([]byte{0x09, 0x14, 0x00})                                                 // RFC 8613 Appendix C.5.
	f.Add([]byte{0x19, 0x14, 0x08, 0x37, 0xcb, 0xf3, 0x21, 0x00, 0x17, 0xa2, 0xd3}) // Appendix C.6.
	f.Add([]byte{0x19, 0x14, 0x02, 0x37, 0xcb, 0x01})
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := UnmarshalOption(data)
		if err != nil {
			return
		}
		encoded, err := decoded.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal decoded option: %v", err)
		}
		roundTrip, err := UnmarshalOption(encoded)
		if err != nil {
			t.Fatalf("unmarshal marshaled option: %v", err)
		}
		if !reflect.DeepEqual(decoded, roundTrip) {
			t.Fatalf("option round-trip mismatch: %#v != %#v", decoded, roundTrip)
		}
	})
}

func FuzzDecodeInner(f *testing.F) {
	f.Add([]byte{0x01, 0xb2, 0x76, 0x31}) // RFC 8613 Appendix C.5 inner request.
	f.Add([]byte{0x01, 0xff, 0x01})
	f.Add([]byte{0x01, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := DecodeInner(data)
		if err != nil {
			return
		}
		encoded, err := EncodeInner(decoded.Code, decoded.Options, decoded.Payload)
		if err != nil {
			t.Fatalf("encode decoded inner message: %v", err)
		}
		roundTrip, err := DecodeInner(encoded)
		if err != nil {
			t.Fatalf("decode encoded inner message: %v", err)
		}
		if !reflect.DeepEqual(decoded, roundTrip) {
			t.Fatalf("inner message round-trip mismatch: %#v != %#v", decoded, roundTrip)
		}
	})
}

func FuzzParseProxyURI(f *testing.F) {
	f.Add("coap://localhost/tv1") // RFC 8613 Appendix C.5 request URI.
	f.Add("coap://[2001:db8::1]:5683/a%2Fb?q=1")
	f.Add("coap://origin.example/")
	f.Add("coap://user@host/")
	f.Fuzz(func(_ *testing.T, uri string) {
		_, _ = ParseProxyURI(uri)
	})
}

func FuzzContextUnprotectRequest(f *testing.F) {
	f.Add([]byte{0x09, 0x14, 0x00}, mustHexSeed("4ed339a5a379b0b8bc731fffb0")) // RFC 8613 Appendix C.5.
	f.Add([]byte{0x09, 0x14}, []byte{0x00})
	f.Add([]byte{0x19, 0x14, 0x02, 0x37, 0xcb, 0x01}, []byte{0x01, 0x02})
	f.Fuzz(func(t *testing.T, option, payload []byte) {
		ctx, err := NewContext(ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          mustHexSeed("0102030405060708090a0b0c0d0e0f10"),
			SenderID:              []byte{1},
			RecipientID:           []byte{0},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = ctx.UnprotectRequest(message.Message{
			Code: codes.POST,
			Options: message.Options{
				{ID: message.URIHost, Value: []byte("localhost")},
				{ID: message.OSCORE, Value: option},
			},
			Payload: payload,
		})
	})
}

func FuzzContextUnprotectResponse(f *testing.F) {
	f.Add([]byte{}, mustHexSeed("dbaad1e9a7e7b2a813d3c31524378303cdafae119106")) // RFC 8613 Appendix C.7.
	f.Add([]byte{0x09, 0x14}, []byte{0x00})
	f.Add([]byte{0x19, 0x14, 0x02, 0x37, 0xcb, 0x01}, []byte{0x01, 0x02})
	f.Fuzz(func(t *testing.T, option, payload []byte) {
		ctx, err := NewContext(ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          mustHexSeed("0102030405060708090a0b0c0d0e0f10"),
			MasterSalt:            mustHexSeed("9e7ca92223786340"),
			SenderID:              []byte{0},
			RecipientID:           []byte{1},
		})
		if err != nil {
			t.Fatal(err)
		}
		exchange := Exchange{responseState: &exchangeResponseState{
			requestID:        []byte{},
			requestPartialIV: mustHexSeed("14"),
			context:          ctx,
		}}
		_, _ = ctx.UnprotectResponse(message.Message{
			Type:    message.Acknowledgement,
			Code:    codes.Changed,
			Token:   message.Token{0x00, 0x00, 0x39, 0x74},
			Options: message.Options{{ID: message.OSCORE, Value: option}},
			Payload: payload,
		}, exchange)
	})
}

func mustHexSeed(value string) []byte {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return decoded
}

func TestFuzzSeedRFCOptionRoundTrip(t *testing.T) {
	input := []byte{0x09, 0x14}
	decoded, err := UnmarshalOption(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := decoded.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, input) {
		t.Fatalf("RFC option round-trip = %x, want %x", encoded, input)
	}
}
