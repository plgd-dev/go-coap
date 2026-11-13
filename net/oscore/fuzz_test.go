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
	f.Add(
		[]byte{0x19, 0x14, 0x08, 0x37, 0xcb, 0xf3, 0x21, 0x00, 0x17, 0xa2, 0xd3},
		mustHexSeed("72cd7273fd331ac45cffbe55c3"),
	) // RFC 8613 Appendix C.6.
	f.Add([]byte{0x09, 0x14}, []byte{0x00})
	f.Add([]byte{0x19, 0x14, 0x02, 0x37, 0xcb, 0x01}, []byte{0x01, 0x02})
	f.Fuzz(func(t *testing.T, option, payload []byte) {
		ctx, err := NewContext(ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          mustHexSeed("0102030405060708090a0b0c0d0e0f10"),
			MasterSalt:            mustHexSeed("9e7ca92223786340"),
			IDContext:             mustHexSeed("37cbf3210017a2d3"),
			SenderID:              []byte{1},
			RecipientID:           []byte{},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, _ = ctx.UnprotectRequest(message.Message{
			Code:  codes.POST,
			Token: message.Token{0xef, 0x9b, 0xbf, 0x7a},
			Options: message.Options{
				{ID: message.URIHost, Value: []byte("localhost")},
				{ID: message.OSCORE, Value: option},
			},
			Payload: payload,
		})
	})
}

func TestFuzzSeedRFC8613AppendixC6AuthenticatesRequest(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHexSeed("0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHexSeed("9e7ca92223786340"),
		IDContext:             mustHexSeed("37cbf3210017a2d3"),
		SenderID:              []byte{1},
		RecipientID:           []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := ctx.UnprotectRequest(message.Message{
		Code:  codes.POST,
		Token: message.Token{0xef, 0x9b, 0xbf, 0x7a},
		Options: message.Options{
			{ID: message.URIHost, Value: []byte("localhost")},
			{ID: message.OSCORE, Value: mustHexSeed("19140837cbf3210017a2d3")},
		},
		Payload: mustHexSeed("72cd7273fd331ac45cffbe55c3"),
	})
	if err != nil {
		t.Fatalf("RFC 8613 Appendix C.6 request did not authenticate: %v", err)
	}
	if plaintext.Code != codes.GET || !reflect.DeepEqual(plaintext.Options, message.Options{
		{ID: message.URIHost, Value: []byte("localhost")},
		{ID: message.URIPath, Value: []byte("tv1")},
	}) {
		t.Fatalf("RFC 8613 Appendix C.6 plaintext = %+v", plaintext)
	}
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
			SenderID:              []byte{},
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

func TestFuzzSeedRFC8613AppendixC7AuthenticatesResponse(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHexSeed("0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHexSeed("9e7ca92223786340"),
		SenderID:              []byte{},
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
	plaintext, err := ctx.UnprotectResponse(message.Message{
		Type:    message.Acknowledgement,
		Code:    codes.Changed,
		Token:   message.Token{0x00, 0x00, 0x39, 0x74},
		Options: message.Options{{ID: message.OSCORE}},
		Payload: mustHexSeed("dbaad1e9a7e7b2a813d3c31524378303cdafae119106"),
	}, exchange)
	if err != nil {
		t.Fatalf("RFC 8613 Appendix C.7 response did not authenticate: %v", err)
	}
	if plaintext.Code != codes.Content || !bytes.Equal(plaintext.Payload, []byte("Hello World!")) {
		t.Fatalf("RFC 8613 Appendix C.7 plaintext = %+v", plaintext)
	}
}

func FuzzUnprotectInnerRequest(f *testing.F) {
	f.Add([]byte{byte(codes.GET)})
	f.Add([]byte{byte(codes.POST), 0xff, 'x'})
	f.Fuzz(func(t *testing.T, inner []byte) {
		sender, recipient := fuzzRequestContexts(t)
		partialIV, ciphertext, err := sender.Seal(inner, func(piv []byte) ([]byte, error) {
			return BuildAADWithAlgorithm(AEADAlgorithmAESCCM16_64_128, sender.senderID, piv, nil)
		})
		if err != nil {
			return
		}
		option, err := (OptionValue{
			PartialIV:   partialIV,
			SenderID:    sender.senderID,
			SenderIDSet: true,
		}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		protected := message.Message{
			Type:  message.Confirmable,
			Code:  codes.POST,
			Token: message.Token{1},
			Options: message.Options{{
				ID: message.OSCORE, Value: option,
			}},
			Payload: ciphertext,
		}
		unprotected, _, err := recipient.UnprotectRequest(protected)
		if err != nil {
			return
		}
		reprotected, _, err := sender.ProtectRequest(unprotected)
		if err != nil {
			t.Fatalf("re-protect authenticated request: %v", err)
		}
		roundTrip, _, err := recipient.UnprotectRequest(reprotected)
		if err != nil {
			t.Fatalf("unprotect re-protected request: %v", err)
		}
		if !sameOSCOREMessage(unprotected, roundTrip) {
			t.Fatalf("request round-trip mismatch: %#v != %#v", unprotected, roundTrip)
		}
	})
}

func FuzzUnprotectInnerResponse(f *testing.F) {
	f.Add([]byte{byte(codes.Content)})
	f.Add([]byte{byte(codes.Changed), 0xff, 'x'})
	f.Fuzz(func(t *testing.T, inner []byte) {
		clientContext, serverContext, clientExchange, serverExchange := fuzzResponseContexts(t)
		partialIV, ciphertext, err := serverContext.SealResponse(inner, func([]byte) ([]byte, error) {
			return BuildAADWithAlgorithm(
				AEADAlgorithmAESCCM16_64_128,
				serverExchange.RequestIdentifier(),
				serverExchange.RequestPartialIVBytes(),
				nil,
			)
		})
		if err != nil {
			return
		}
		option, err := (OptionValue{
			PartialIV:   partialIV,
			SenderID:    serverContext.senderID,
			SenderIDSet: true,
		}).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		protected := message.Message{
			Type:  message.Acknowledgement,
			Code:  codes.Changed,
			Token: message.Token{1},
			Options: message.Options{{
				ID: message.OSCORE, Value: option,
			}},
			Payload: ciphertext,
		}
		unprotected, err := clientContext.UnprotectResponse(protected, clientExchange)
		if err != nil {
			return
		}
		clientContext, serverContext, clientExchange, serverExchange = fuzzResponseContexts(t)
		reprotected, err := serverContext.ProtectResponse(unprotected, serverExchange)
		if err != nil {
			t.Fatalf("re-protect authenticated response: %v", err)
		}
		roundTrip, err := clientContext.UnprotectResponse(reprotected, clientExchange)
		if err != nil {
			t.Fatalf("unprotect re-protected response: %v", err)
		}
		if !sameOSCOREMessage(unprotected, roundTrip) {
			t.Fatalf("response round-trip mismatch: %#v != %#v", unprotected, roundTrip)
		}
	})
}

func fuzzRequestContexts(t *testing.T) (*Context, *Context) {
	t.Helper()
	const secret = "OSCORE fuzz post-authentication"
	sender, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte(secret),
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte(secret),
		SenderID:              []byte{1},
		RecipientID:           []byte{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender, recipient
}

func fuzzResponseContexts(t *testing.T) (*Context, *Context, Exchange, Exchange) {
	t.Helper()
	clientContext, serverContext := fuzzRequestContexts(t)
	request, clientExchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := serverContext.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	return clientContext, serverContext, clientExchange, serverExchange
}

func sameOSCOREMessage(a, b message.Message) bool {
	return a.Code == b.Code && bytes.Equal(a.Token, b.Token) &&
		reflect.DeepEqual(a.Options, b.Options) && bytes.Equal(a.Payload, b.Payload)
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
