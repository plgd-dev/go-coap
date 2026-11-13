package oscore

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func pairedContexts(t *testing.T, clientSequence, serverSequence uint64) (*Context, *Context) {
	t.Helper()
	masterSecret := mustHex(t, "0102030405060708090a0b0c0d0e0f10")
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
		SenderSequenceNumber:  clientSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		SenderID:              []byte{1},
		RecipientID:           []byte{0},
		SenderSequenceNumber:  serverSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func TestProtectRequestAndResponse(t *testing.T) {
	client, server := pairedContexts(t, 3, 5)
	request := message.Message{
		Token:     message.Token{0x12, 0x34},
		Code:      codes.GET,
		Type:      message.Confirmable,
		MessageID: 0x1234,
		Options: message.Options{
			{ID: message.URIHost, Value: []byte("coap.example")},
			{ID: message.URIPath, Value: []byte("sensors")},
			{ID: message.ContentFormat, Value: []byte{byte(message.AppCBOR)}},
		},
		Payload: []byte{0xa1, 0x01, 0x02},
	}
	_, clientExchange, _, serverExchange := assertProtectedRequestRoundTrip(t, client, server, request)
	response := message.Message{
		Token:     request.Token,
		Code:      codes.Content,
		Type:      message.Acknowledgement,
		MessageID: request.MessageID,
		Options: message.Options{
			{ID: message.ContentFormat, Value: []byte{byte(message.AppCBOR)}},
		},
		Payload: []byte{0xa1, 0x03, 0x04},
	}
	assertProtectedResponseRoundTrip(t, client, server, clientExchange, serverExchange, response)
}

func assertProtectedRequestRoundTrip(
	t *testing.T, client, server *Context, request message.Message,
) (message.Message, Exchange, message.Message, Exchange) {
	t.Helper()
	protectedRequest, clientExchange, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if protectedRequest.Code != codes.POST || protectedRequest.Type != request.Type ||
		protectedRequest.MessageID != request.MessageID || !bytes.Equal(protectedRequest.Token, request.Token) {
		t.Fatalf("unexpected protected request envelope: %+v", protectedRequest)
	}
	if !protectedRequest.Options.HasOption(message.OSCORE) || !protectedRequest.Options.HasOption(message.URIHost) ||
		protectedRequest.Options.HasOption(message.URIPath) {
		t.Fatalf("protected request options = %+v", protectedRequest.Options)
	}
	if protectedRequest.Options.HasOption(message.ContentFormat) {
		t.Fatal("OSCORE request must not expose the inner Content-Format")
	}

	openedRequest, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if openedRequest.Code != request.Code || openedRequest.Type != request.Type ||
		openedRequest.MessageID != request.MessageID || !bytes.Equal(openedRequest.Token, request.Token) ||
		!bytes.Equal(openedRequest.Payload, request.Payload) || !optionsEqual(openedRequest.Options, request.Options) {
		t.Fatalf("opened request = %+v, want %+v", openedRequest, request)
	}
	if !bytes.Equal(clientExchange.RequestIdentifier(), serverExchange.RequestIdentifier()) ||
		!bytes.Equal(clientExchange.RequestPartialIVBytes(), serverExchange.RequestPartialIVBytes()) {
		t.Fatalf("request exchange mismatch: client=%+v server=%+v", clientExchange, serverExchange)
	}
	return protectedRequest, clientExchange, openedRequest, serverExchange
}

func assertProtectedResponseRoundTrip(
	t *testing.T, client, server *Context, clientExchange, serverExchange Exchange,
	response message.Message,
) {
	t.Helper()
	protectedResponse, err := server.ProtectResponse(response, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	if protectedResponse.Code != codes.Changed || !protectedResponse.Options.HasOption(message.OSCORE) {
		t.Fatalf("unexpected protected response: %+v", protectedResponse)
	}
	responseOption, err := UnmarshalOption(optionValue(t, protectedResponse.Options, message.OSCORE))
	if err != nil {
		t.Fatal(err)
	}
	if responseOption.SenderIDSet || responseOption.IDContextSet {
		t.Fatalf("response option unexpectedly contains KID or ID Context: %+v", responseOption)
	}
	if len(responseOption.PartialIV) != 0 {
		t.Fatalf("ordinary response contains Partial IV: %x", responseOption.PartialIV)
	}
	nextServerPIV, err := server.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nextServerPIV, []byte{5}) {
		t.Fatalf("ordinary response consumed a sender sequence number: next PIV %x", nextServerPIV)
	}

	openedResponse, err := client.UnprotectResponse(protectedResponse, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if openedResponse.Code != response.Code || openedResponse.Type != response.Type ||
		openedResponse.MessageID != response.MessageID || !bytes.Equal(openedResponse.Token, response.Token) ||
		!bytes.Equal(openedResponse.Payload, response.Payload) || !optionsEqual(openedResponse.Options, response.Options) {
		t.Fatalf("opened response = %+v, want %+v", openedResponse, response)
	}
}

func TestProtectRequestAndResponseWithAESCCM256(t *testing.T) {
	masterSecret := mustHex(t, "0102030405060708090a0b0c0d0e0f10")
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		Algorithm:             AEADAlgorithmAESCCM16_128_256,
		MasterSecret:          masterSecret,
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		Algorithm:             AEADAlgorithmAESCCM16_128_256,
		MasterSecret:          masterSecret,
		SenderID:              []byte{1},
		RecipientID:           []byte{0},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := message.Message{Code: codes.GET}
	protectedRequest, exchange, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	openedRequest, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if openedRequest.Code != request.Code {
		t.Fatalf("opened request code = %v, want %v", openedRequest.Code, request.Code)
	}

	response := message.Message{Code: codes.Content}
	protectedResponse, err := server.ProtectResponse(response, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	openedResponse, err := client.UnprotectResponse(protectedResponse, exchange)
	if err != nil {
		t.Fatal(err)
	}
	if openedResponse.Code != response.Code {
		t.Fatalf("opened response code = %v, want %v", openedResponse.Code, response.Code)
	}
}

func TestProtectQBlockRequestAndResponse(t *testing.T) {
	client, server := pairedContexts(t, 3, 5)
	request := message.Message{
		Token: message.Token{0x12, 0x34},
		Code:  codes.GET,
		Options: message.Options{
			{ID: message.URIPath, Value: []byte("large")},
			{ID: message.QBlock2, Value: []byte{0x0e}},
		},
	}
	protectedRequest, clientExchange, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if protectedRequest.Options.HasOption(message.QBlock2) {
		t.Fatal("Q-Block2 option was exposed outside the protected request")
	}
	openedRequest, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !optionsEqual(openedRequest.Options, request.Options) {
		t.Fatalf("opened Q-Block request options = %+v, want %+v", openedRequest.Options, request.Options)
	}

	response := message.Message{
		Token: request.Token,
		Code:  codes.Content,
		Options: message.Options{
			{ID: message.Size2, Value: []byte{0x20}},
			{ID: message.QBlock2, Value: []byte{0x06}},
		},
		Payload: []byte("block"),
	}
	protectedResponse, err := server.ProtectResponseWithPartialIV(response, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	if protectedResponse.Options.HasOption(message.QBlock2) {
		t.Fatal("Q-Block2 option was exposed outside the protected response")
	}
	openedResponse, err := client.UnprotectResponse(protectedResponse, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if !optionsEqual(openedResponse.Options, response.Options) || !bytes.Equal(openedResponse.Payload, response.Payload) {
		t.Fatalf("opened Q-Block response = %+v, want %+v", openedResponse, response)
	}
}

func TestProtectObserveRequestAndNotifications(t *testing.T) {
	client, server := pairedContexts(t, 3, 5)
	request := message.Message{Code: codes.GET, Token: message.Token{0x12, 0x34}, Options: observeOptions(t, 0)}
	observation := assertObserveRegistration(t, client, server, request)
	assertObserveNotification(t, observation, 20, 5)
	assertObserveNotification(t, observation, 21, 6)
	assertObserveCancellation(t, client, server)
}

func TestProtectFetchObserveRequest(t *testing.T) {
	client, server := pairedContexts(t, 3, 5)
	request := message.Message{
		Code:    codes.FETCH,
		Token:   message.Token{0x56},
		Options: observeOptions(t, 0),
		Payload: []byte("query"),
	}
	protected, _, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if protected.Code != codes.FETCH {
		t.Fatalf("outer request code = %v, want FETCH", protected.Code)
	}
	opened, exchange, err := server.UnprotectRequest(protected)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != codes.FETCH || !exchange.IsObserve() || !optionsEqual(opened.Options, request.Options) {
		t.Fatalf("opened FETCH Observe request = %+v, exchange %+v", opened, exchange)
	}
}

type observeTestExchange struct {
	client         *Context
	server         *Context
	request        message.Message
	clientExchange Exchange
	serverExchange Exchange
}

func observeOptions(t *testing.T, value uint32) message.Options {
	t.Helper()
	options, _, err := (message.Options{}).SetObserve(make([]byte, 4), value)
	if err != nil {
		t.Fatal(err)
	}
	return options
}

func TestValidateInnerRequestObserveValue(t *testing.T) {
	for _, value := range []uint32{0, 1} {
		assertValidObserveRequest(t, value)
	}

	for _, test := range []struct {
		name  string
		value []byte
		want  uint32
	}{
		{name: "zero with leading zero", value: []byte{0}, want: 0},
		{name: "one with leading zero", value: []byte{0, 1}, want: 1},
		{name: "one with multiple leading zeros", value: []byte{0, 0, 1}, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) { assertLeadingZeroObserveRequest(t, test.value, test.want) })
	}

	for _, value := range []uint32{2, 3, 0xffffff} {
		assertInvalidObserveRequest(t, value)
	}

	assertObservePostRejected(t)
	assertMalformedObserveRejected(t)
}

func assertValidObserveRequest(t *testing.T, value uint32) {
	t.Helper()
	options := observeOptions(t, value)
	outerOption, hasOuterObserve, err := findObserveOption(options)
	if err != nil || !hasOuterObserve {
		t.Fatalf("find Observe option for %d: has=%t err=%v", value, hasOuterObserve, err)
	}
	if _, err := validateInnerRequest(message.Message{Code: codes.GET, Options: options}, outerOption, true); err != nil {
		t.Errorf("valid Observe value %d rejected: %v", value, err)
	}
}

func assertLeadingZeroObserveRequest(t *testing.T, value []byte, want uint32) {
	t.Helper()
	outer := message.Option{ID: message.Observe, Value: value}
	inner := message.Message{
		Code:    codes.GET,
		Options: message.Options{{ID: message.Observe, Value: []byte{byte(want)}}},
	}
	if want == 0 {
		inner.Options[0].Value = nil
	}
	if _, err := validateInnerRequest(inner, outer, true); err != nil {
		t.Fatalf("leading-zero Observe request rejected: %v", err)
	}
	got, err := observeOptionValue(outer)
	if err != nil || got != want {
		t.Fatalf("decoded Observe = %d, %v; want %d", got, err, want)
	}
}

func assertInvalidObserveRequest(t *testing.T, value uint32) {
	t.Helper()
	options := observeOptions(t, value)
	outerOption, hasOuterObserve, err := findObserveOption(options)
	if err != nil || !hasOuterObserve {
		t.Fatalf("find Observe option for %d: has=%t err=%v", value, hasOuterObserve, err)
	}
	if _, err := validateInnerRequest(message.Message{Code: codes.GET, Options: options}, outerOption, true); !errors.Is(err, ErrInvalidOSCOREMessage) {
		t.Errorf("invalid Observe value %d error = %v, want %v", value, err, ErrInvalidOSCOREMessage)
	}
}

func assertObservePostRejected(t *testing.T) {
	t.Helper()
	options := observeOptions(t, 0)
	outerOption, _, err := findObserveOption(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateInnerRequest(message.Message{Code: codes.POST, Options: options}, outerOption, true); !errors.Is(err, ErrUnsupportedOption) {
		t.Fatalf("POST with Observe error = %v, want ErrUnsupportedOption", err)
	}
}

func assertMalformedObserveRejected(t *testing.T) {
	t.Helper()
	options := message.Options{{ID: message.Observe, Value: []byte{0, 0, 0, 0, 0}}}
	outerOption, _, err := findObserveOption(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateInnerRequest(message.Message{Code: codes.GET, Options: options}, outerOption, true); !errors.Is(err, ErrInvalidOSCOREMessage) {
		t.Fatalf("malformed Observe error = %v, want ErrInvalidOSCOREMessage", err)
	}
}

func assertObserveRegistration(
	t *testing.T, client, server *Context, request message.Message,
) observeTestExchange {
	t.Helper()
	protectedRequest, clientExchange, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if protectedRequest.Code != codes.FETCH {
		t.Fatalf("outer request code = %v, want FETCH", protectedRequest.Code)
	}
	if got, observeErr := protectedRequest.Options.Observe(); observeErr != nil || got != 0 {
		t.Fatalf("outer Observe = %d, %v; want 0", got, observeErr)
	}
	openedRequest, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !serverExchange.IsObserve() || !optionsEqual(openedRequest.Options, request.Options) {
		t.Fatalf("opened Observe request = %+v, exchange %+v", openedRequest, serverExchange)
	}
	return observeTestExchange{
		client: client, server: server, request: request,
		clientExchange: clientExchange, serverExchange: serverExchange,
	}
}

func assertObserveNotification(t *testing.T, exchange observeTestExchange, responseSequence, wantSequence uint32) {
	t.Helper()
	protectedResponse, err := exchange.server.ProtectResponse(message.Message{
		Code:    codes.Content,
		Token:   exchange.request.Token,
		Options: observeOptions(t, responseSequence),
	}, exchange.serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	if protectedResponse.Code != codes.Content {
		t.Fatalf("outer response code = %v, want Content", protectedResponse.Code)
	}
	responseOption, err := UnmarshalOption(optionValue(t, protectedResponse.Options, message.OSCORE))
	if err != nil {
		t.Fatal(err)
	}
	if len(responseOption.PartialIV) == 0 {
		t.Fatal("Observe notification omitted Partial IV")
	}
	outerObserve, err := protectedResponse.Options.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if outerObserve != responseSequence {
		t.Fatalf("outer Observe = %d, want %d", outerObserve, responseSequence)
	}
	openedResponse, err := exchange.client.UnprotectResponse(protectedResponse, exchange.clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := openedResponse.Options.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if sequence != wantSequence {
		t.Fatalf("decrypted Observe = %d, want Partial-IV sequence %d", sequence, wantSequence)
	}
}

func assertObserveCancellation(t *testing.T, client, server *Context) {
	t.Helper()
	cancelRequest, _, err := client.ProtectRequest(message.Message{
		Code: codes.GET, Options: observeOptions(t, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	outerObserve, err := cancelRequest.Options.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if outerObserve != 1 {
		t.Fatalf("cancellation outer Observe = %d, want 1", outerObserve)
	}
	openedCancel, cancelExchange, err := server.UnprotectRequest(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	cancelObserve, err := openedCancel.Options.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if cancelObserve != 1 || cancelExchange.IsObserve() {
		t.Fatalf("decrypted cancellation Observe = %d; exchange %+v", cancelObserve, cancelExchange)
	}
}

func TestRFC8613AppendixC5ProtectedRequest(t *testing.T) {
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		SenderID:              mustHex(t, "00"),
		RecipientID:           mustHex(t, "01"),
		SenderSequenceNumber:  20,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := message.Message{
		Type:      message.Confirmable,
		Code:      codes.GET,
		MessageID: 0x71c3,
		Token:     message.Token{0x00, 0x00, 0xb9, 0x32},
		Options: message.Options{
			{ID: message.URIHost, Value: []byte("localhost")},
			{ID: message.URIPath, Value: []byte("tv1")},
		},
	}

	protected, _, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	assertRFC8613ProtectedRequest(t, protected,
		0x71c3, "0000b932", "091400", "4ed339a5a379b0b8bc731fffb0")
}

func TestRFC8613AppendixC6ProtectedRequestWithIDContext(t *testing.T) {
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHex(t, "9e7ca92223786340"),
		IDContext:             mustHex(t, "37cbf3210017a2d3"),
		SenderID:              []byte{},
		RecipientID:           mustHex(t, "01"),
		SenderSequenceNumber:  20,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := message.Message{
		Type:      message.Confirmable,
		Code:      codes.GET,
		MessageID: 0x2f8e,
		Token:     message.Token{0xef, 0x9b, 0xbf, 0x7a},
		Options: message.Options{
			{ID: message.URIHost, Value: []byte("localhost")},
			{ID: message.URIPath, Value: []byte("tv1")},
		},
	}

	protected, _, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	assertRFC8613ProtectedRequest(t, protected,
		0x2f8e, "ef9bbf7a", "19140837cbf3210017a2d3", "72cd7273fd331ac45cffbe55c3")
}

func assertRFC8613ProtectedRequest(t *testing.T, protected message.Message, messageID int32, tokenHex, optionHex, payloadHex string) {
	t.Helper()
	if protected.Type != message.Confirmable || protected.Code != codes.POST ||
		protected.MessageID != messageID || !bytes.Equal(protected.Token, mustHex(t, tokenHex)) {
		t.Fatalf("unexpected protected request envelope: %+v", protected)
	}
	if got := optionValue(t, protected.Options, message.OSCORE); !bytes.Equal(got, mustHex(t, optionHex)) {
		t.Fatalf("OSCORE option = %x, want %s", got, optionHex)
	}
	if got := optionValue(t, protected.Options, message.URIHost); !bytes.Equal(got, []byte("localhost")) {
		t.Fatalf("outer Uri-Host = %q, want localhost", got)
	}
	if protected.Options.HasOption(message.URIPath) {
		t.Fatal("Uri-Path must be encrypted")
	}
	if !bytes.Equal(protected.Payload, mustHex(t, payloadHex)) {
		t.Fatalf("ciphertext = %x, want %s", protected.Payload, payloadHex)
	}
}

func TestRFC8613AppendixC7ProtectedResponse(t *testing.T) {
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHex(t, "9e7ca92223786340"),
		SenderID:              mustHex(t, "01"),
		RecipientID:           []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHex(t, "9e7ca92223786340"),
		SenderID:              []byte{},
		RecipientID:           mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	exchange := Exchange{
		responseState: &exchangeResponseState{
			requestID:        []byte{},
			requestPartialIV: mustHex(t, "14"),
			context:          server,
		},
	}
	clientExchange := Exchange{
		responseState: &exchangeResponseState{
			requestID:        []byte{},
			requestPartialIV: mustHex(t, "14"),
			context:          client,
		},
	}
	response := message.Message{
		Type:      message.Acknowledgement,
		Code:      codes.Content,
		MessageID: 0x5d1f,
		Token:     message.Token{0x00, 0x00, 0x39, 0x74},
		Payload:   []byte("Hello World!"),
	}

	protected, err := server.ProtectResponse(response, exchange)
	if err != nil {
		t.Fatal(err)
	}
	if protected.Type != response.Type || protected.Code != codes.Changed ||
		protected.MessageID != response.MessageID || !bytes.Equal(protected.Token, response.Token) {
		t.Fatalf("unexpected protected response envelope: %+v", protected)
	}
	if got := optionValue(t, protected.Options, message.OSCORE); len(got) != 0 {
		t.Fatalf("response OSCORE option = %x, want empty", got)
	}
	if want := mustHex(t, "dbaad1e9a7e7b2a813d3c31524378303cdafae119106"); !bytes.Equal(protected.Payload, want) {
		t.Fatalf("response ciphertext = %x, want %x", protected.Payload, want)
	}
	opened, err := client.UnprotectResponse(protected, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != response.Code || !bytes.Equal(opened.Payload, response.Payload) {
		t.Fatalf("opened response = %+v, want code %v and payload %q", opened, response.Code, response.Payload)
	}
	if _, err := client.UnprotectResponse(protected, clientExchange); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed response error = %v, want ErrReplay", err)
	}
}

func TestUnprotectResponseWithoutPartialIVRejectsReplay(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ProtectResponse(
		message.Message{Code: codes.Content, Payload: []byte("response")},
		serverExchange,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.ProtectResponse(
		message.Message{Code: codes.Content, Payload: []byte("second response")},
		serverExchange,
	); !errors.Is(err, ErrReplay) {
		t.Fatalf("reused response nonce error = %v, want ErrReplay", err)
	}
	exchangeCopy := clientExchange
	if _, err := client.UnprotectResponse(response, exchangeCopy); err != nil {
		t.Fatalf("first response failed: %v", err)
	}
	if _, replayErr := client.UnprotectResponse(response, clientExchange); !errors.Is(replayErr, ErrReplay) {
		t.Fatalf("replayed response error = %v, want ErrReplay", replayErr)
	}
}

func TestExchangeBindingIsImmutableAndSharedByCopies(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	expectedID := serverExchange.RequestIdentifier()
	expectedPartialIV := serverExchange.RequestPartialIVBytes()
	serverExchange.RequestIdentifier()[0] ^= 0xff
	serverExchange.RequestPartialIVBytes()[0] ^= 0xff
	if !bytes.Equal(serverExchange.RequestIdentifier(), expectedID) ||
		!bytes.Equal(serverExchange.RequestPartialIVBytes(), expectedPartialIV) {
		t.Fatal("mutating accessor results changed the Exchange binding")
	}

	exchangeCopy := serverExchange
	protected, err := server.ProtectResponse(message.Message{
		Code: codes.Content, Token: request.Token, Payload: []byte("response"),
	}, exchangeCopy)
	if err != nil {
		t.Fatal(err)
	}
	if _, unprotectErr := client.UnprotectResponse(protected, clientExchange); unprotectErr != nil {
		t.Fatalf("response did not use the immutable request binding: %v", unprotectErr)
	}
	if _, protectErr := server.ProtectResponse(message.Message{
		Code: codes.Content, Token: request.Token, Payload: []byte("different"),
	}, serverExchange); !errors.Is(protectErr, ErrReplay) {
		t.Fatalf("second response protection error = %v, want ErrReplay", protectErr)
	}

	if _, protectErr := server.ProtectResponse(message.Message{Code: codes.Content}, Exchange{}); !errors.Is(protectErr, ErrInvalidContext) {
		t.Fatalf("zero-value Exchange error = %v, want ErrInvalidContext", protectErr)
	}
	unrelated, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("unrelated context"),
		SenderID:              []byte{2},
		RecipientID:           []byte{3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unrelated.ProtectResponse(message.Message{Code: codes.Content}, serverExchange); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("cross-context Exchange error = %v, want ErrInvalidContext", err)
	}
}

func TestProtectResponseFailureDoesNotConsumeOrdinaryExchange(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protectedRequest, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, exchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, server.senderAEAD.MaxLength()+1)
	if _, err := server.ProtectResponse(message.Message{
		Code: codes.Content, Payload: oversized,
	}, exchange); !errors.Is(err, ErrPlaintextTooLarge) {
		t.Fatalf("oversized response error = %v, want ErrPlaintextTooLarge", err)
	}
	if _, err := server.ProtectResponse(message.Message{
		Code: codes.Content, Payload: []byte("retry"),
	}, exchange); err != nil {
		t.Fatalf("response after failed encryption: %v", err)
	}
}

func TestProtectResponseSequenceReservationFailureCanRetry(t *testing.T) {
	const masterSecret = "response sequence reservation retry"
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte(masterSecret),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	if err != nil {
		t.Fatal(err)
	}
	storeErr := errors.New("temporary sequence storage failure")
	store := &testSenderSequenceStore{err: storeErr}
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte(masterSecret),
		SenderID:              []byte("server"),
		RecipientID:           []byte("client"),
		SenderSequenceStore:   store,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, exchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response := message.Message{Code: codes.Content, Payload: []byte("retry")}
	if _, err := server.ProtectResponseWithPartialIV(response, exchange); !errors.Is(err, storeErr) {
		t.Fatalf("response sequence reservation error = %v, want %v", err, storeErr)
	}
	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	if _, err := server.ProtectResponseWithPartialIV(response, exchange); err != nil {
		t.Fatalf("response after sequence store recovered: %v", err)
	}
}

func TestProtectResponseWithPartialIVIsOneShotForOrdinaryExchange(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, exchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response := message.Message{Code: codes.Content, Payload: []byte("first")}
	if _, err := server.ProtectResponseWithPartialIV(response, exchange); err != nil {
		t.Fatal(err)
	}
	response.Payload = []byte("different")
	if _, err := server.ProtectResponseWithPartialIV(response, exchange); !errors.Is(err, ErrReplay) {
		t.Fatalf("second Partial-IV response error = %v, want ErrReplay", err)
	}
}

func TestConcurrentProtectResponseUsesOrdinaryNonceOnce(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, exchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, payload := range []string{"first", "second"} {
		wg.Add(1)
		go func(payload string) {
			defer wg.Done()
			_, protectErr := server.ProtectResponse(message.Message{
				Code: codes.Content, Payload: []byte(payload),
			}, exchange)
			errs <- protectErr
		}(payload)
	}
	wg.Wait()
	close(errs)

	var protected, rejected int
	for protectErr := range errs {
		switch {
		case protectErr == nil:
			protected++
		case errors.Is(protectErr, ErrReplay):
			rejected++
		default:
			t.Fatalf("unexpected concurrent response protection error: %v", protectErr)
		}
	}
	if protected != 1 || rejected != 1 {
		t.Fatalf("concurrent protections: protected=%d rejected=%d; want one of each", protected, rejected)
	}
}

func TestConcurrentUnprotectOrdinaryResponseAcceptsOnce(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protectedRequest, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ProtectResponse(message.Message{
		Code: codes.Content, Payload: []byte("response"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unprotectErr := client.UnprotectResponse(response, clientExchange)
			errs <- unprotectErr
		}()
	}
	wg.Wait()
	close(errs)

	var accepted, rejected int
	for unprotectErr := range errs {
		switch {
		case unprotectErr == nil:
			accepted++
		case errors.Is(unprotectErr, ErrReplay):
			rejected++
		default:
			t.Fatalf("unexpected concurrent response acceptance error: %v", unprotectErr)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent response acceptance: accepted=%d rejected=%d; want one of each", accepted, rejected)
	}
}

func TestObserveAllowsInitialResponseWithoutPartialIV(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	observeOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	request, clientExchange, err := client.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{1},
		Options: observeOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := EncodeInner(codes.Content, message.Options{{ID: message.Observe}}, []byte("initial"))
	if err != nil {
		t.Fatal(err)
	}
	aad, err := BuildAADWithAlgorithm(server.algorithm, serverExchange.RequestIdentifier(), serverExchange.RequestPartialIVBytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := server.SealResponseWithRequestNonce(
		inner,
		serverExchange.RequestPartialIVBytes(),
		func([]byte) ([]byte, error) { return aad, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	firstResponse := message.Message{
		Code:    codes.Content,
		Token:   request.Token,
		Options: message.Options{{ID: message.OSCORE}},
		Payload: ciphertext,
	}
	opened, err := client.UnprotectResponse(firstResponse, clientExchange)
	if err != nil {
		t.Fatalf("first Observe response without Partial IV failed: %v", err)
	}
	if got, observeErr := opened.Options.Observe(); observeErr != nil || got != 0x00ffffff {
		t.Fatalf("Observe sequence = %d, error = %v, want 0xffffff", got, observeErr)
	}
	if _, replayErr := client.UnprotectResponse(firstResponse, clientExchange); !errors.Is(replayErr, ErrReplay) {
		t.Fatalf("replayed initial Observe response error = %v, want ErrReplay", replayErr)
	}

	notificationOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 1)
	if err != nil {
		t.Fatal(err)
	}
	notification, err := server.ProtectResponseWithPartialIV(message.Message{
		Code:    codes.Content,
		Token:   request.Token,
		Options: notificationOptions,
		Payload: []byte("notification"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UnprotectResponse(notification, clientExchange); err != nil {
		t.Fatalf("notification with Partial IV after initial response failed: %v", err)
	}
}

func TestObserveNotificationWithoutOuterObserveUsesInnerObserveAndPartialIV(t *testing.T) {
	client, server := pairedContexts(t, 0, 0x1234)
	request, clientExchange, err := client.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{0x41},
		Options: observeOptions(t, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	protectedNotification, err := server.ProtectResponseWithPartialIV(message.Message{
		Code:    codes.Content,
		Token:   request.Token,
		Options: message.Options{{ID: message.Observe}},
		Payload: []byte("notification"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	protectedNotification.Options = removeObserveOption(protectedNotification.Options)

	notification, err := client.UnprotectResponse(protectedNotification, clientExchange)
	if err != nil {
		t.Fatalf("notification without Outer Observe failed: %v", err)
	}
	if got, err := notification.Options.Observe(); err != nil || got != 0x1234 {
		t.Fatalf("reconstructed Observe value = %d, error = %v; want Partial IV 0x1234", got, err)
	}
	if !bytes.Equal(notification.Payload, []byte("notification")) {
		t.Fatalf("notification payload = %q", notification.Payload)
	}
}

func TestRFC8613AppendixC8ProtectedResponseWithPartialIV(t *testing.T) {
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHex(t, "9e7ca92223786340"),
		SenderID:              mustHex(t, "01"),
		RecipientID:           []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          mustHex(t, "0102030405060708090a0b0c0d0e0f10"),
		MasterSalt:            mustHex(t, "9e7ca92223786340"),
		SenderID:              []byte{},
		RecipientID:           mustHex(t, "01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	exchange := Exchange{
		responseState: &exchangeResponseState{
			requestID:        []byte{},
			requestPartialIV: mustHex(t, "14"),
			context:          server,
		},
	}
	clientExchange := Exchange{
		responseState: &exchangeResponseState{
			requestID:        []byte{},
			requestPartialIV: mustHex(t, "14"),
			context:          client,
		},
	}
	response := message.Message{
		Type:      message.Acknowledgement,
		Code:      codes.Content,
		MessageID: 0x5d1f,
		Token:     message.Token{0x00, 0x00, 0x39, 0x74},
		Payload:   []byte("Hello World!"),
	}

	protected, err := server.ProtectResponseWithPartialIV(response, exchange)
	if err != nil {
		t.Fatal(err)
	}
	if got := optionValue(t, protected.Options, message.OSCORE); !bytes.Equal(got, mustHex(t, "0100")) {
		t.Fatalf("response OSCORE option = %x, want 0100", got)
	}
	if want := mustHex(t, "4d4c13669384b67354b2b6175ff4b8658c666a6cf88e"); !bytes.Equal(protected.Payload, want) {
		t.Fatalf("response ciphertext = %x, want %x", protected.Payload, want)
	}
	nextPartialIV, err := server.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nextPartialIV, mustHex(t, "01")) {
		t.Fatalf("next sender Partial IV = %x, want 01", nextPartialIV)
	}
	opened, err := client.UnprotectResponse(protected, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != response.Code || !bytes.Equal(opened.Payload, response.Payload) {
		t.Fatalf("opened response = %+v, want code %v and payload %q", opened, response.Code, response.Payload)
	}
}

func TestProtectWithPresentEmptyIDContext(t *testing.T) {
	masterSecret := []byte{1, 2, 3, 4}
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{},
		SenderID:              []byte{},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{},
		SenderID:              []byte{1},
		RecipientID:           []byte{},
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	option, err := UnmarshalOption(optionValue(t, protected.Options, message.OSCORE))
	if err != nil {
		t.Fatal(err)
	}
	if !option.IDContextSet || len(option.IDContext) != 0 || !option.SenderIDSet || len(option.SenderID) != 0 {
		t.Fatalf("decoded option does not preserve present empty identifiers: %+v", option)
	}
	if _, _, err := server.UnprotectRequest(protected); err != nil {
		t.Fatal(err)
	}
}

func TestResponsePartialIVUsesResponseSenderID(t *testing.T) {
	client, server := pairedContexts(t, 0, 7)
	request, exchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	aad, err := BuildAAD(serverExchange.RequestIdentifier(), serverExchange.RequestPartialIVBytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	responsePIV, ciphertext, err := server.SealResponse([]byte{byte(codes.Content)}, func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	serverNonce, err := server.nonce(responsePIV, server.senderID)
	if err != nil {
		t.Fatal(err)
	}
	clientNonce, err := client.nonce(responsePIV, client.recipientID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(serverNonce, clientNonce) {
		t.Fatalf("response nonce differs: server=%x client=%x", serverNonce, clientNonce)
	}
	opened, err := client.OpenResponse(responsePIV, ciphertext, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, []byte{byte(codes.Content)}) {
		t.Fatalf("opened response = %x", opened)
	}

	if len(exchange.RequestPartialIVBytes()) == 0 {
		t.Fatal("request exchange has no Partial IV")
	}
}

func TestUnprotectRejectsTamperingAndReplay(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	tampered := protected
	tampered.Payload = append([]byte(nil), protected.Payload...)
	tampered.Payload[0] ^= 0x80
	if _, _, err := server.UnprotectRequest(tampered); err == nil {
		t.Fatal("expected authentication failure")
	}
	if _, _, err := server.UnprotectRequest(protected); err != nil {
		t.Fatalf("failed authentication consumed replay sequence: %v", err)
	}
	if _, _, err := server.UnprotectRequest(protected); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed request error = %v, want ErrReplay", err)
	}
}

func TestObserveRejectsOutOfOrderFullWidthPartialIV(t *testing.T) {
	requestContext, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("OSCORE observe sequence test"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverContext, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("OSCORE observe sequence test"),
		SenderID:              []byte("server"),
		RecipientID:           []byte("client"),
		SenderSequenceNumber:  0xffffff,
	})
	if err != nil {
		t.Fatal(err)
	}

	requestOptions := observeOptions(t, 0)
	protectedRequest, clientExchange, err := requestContext.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{1},
		Options: requestOptions,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := serverContext.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	makeResponse := func(body string) message.Message {
		t.Helper()
		response, protectErr := serverContext.ProtectResponseWithPartialIV(message.Message{
			Code:    codes.Content,
			Token:   protectedRequest.Token,
			Options: message.Options{{ID: message.Observe}},
			Payload: []byte(body),
		}, serverExchange)
		if protectErr != nil {
			t.Fatal(protectErr)
		}
		return response
	}
	lowerResponse := makeResponse("lower")
	higherResponse := makeResponse("higher")
	if _, err := requestContext.UnprotectResponse(higherResponse, clientExchange); err != nil {
		t.Fatalf("newer full-width Partial IV response failed: %v", err)
	}
	if _, err := requestContext.UnprotectResponse(lowerResponse, clientExchange); !errors.Is(err, ErrReplay) {
		t.Fatalf("out-of-order response error = %v, want ErrReplay", err)
	}
}

func TestUnprotectRejectsMalformedOuterMessage(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*message.Message){
		"missing KID": func(msg *message.Message) {
			msg.Options = msg.Options.Set(message.Option{ID: message.OSCORE, Value: []byte{1}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			msg := protected
			msg.Options = CloneOptions(protected.Options)
			mutate(&msg)
			if _, _, err := server.UnprotectRequest(msg); err == nil {
				t.Fatal("expected malformed OSCORE message to fail")
			}
		})
	}
}

func TestUnprotectDiscardsOuterCode(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protectedRequest, exchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	protectedRequest.Code = codes.PUT
	plaintextRequest, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatalf("UnprotectRequest after outer Code change: %v", err)
	}
	if plaintextRequest.Code != codes.GET {
		t.Fatalf("decrypted request Code = %v, want GET", plaintextRequest.Code)
	}

	protectedResponse, err := server.ProtectResponse(
		message.Message{Code: codes.Content, Payload: []byte("response")},
		serverExchange,
	)
	if err != nil {
		t.Fatal(err)
	}
	protectedResponse.Code = codes.NotFound
	plaintextResponse, err := client.UnprotectResponse(protectedResponse, exchange)
	if err != nil {
		t.Fatalf("UnprotectResponse after outer Code change: %v", err)
	}
	if plaintextResponse.Code != codes.Content {
		t.Fatalf("decrypted response Code = %v, want Content", plaintextResponse.Code)
	}
}

func TestUnprotectDiscardsClassEOptionsFromOuterMessages(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protectedRequest, exchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	protectedRequest.Options = append(protectedRequest.Options,
		message.Option{ID: message.URIPath, Value: []byte("unexpected")},
		message.Option{ID: message.ContentFormat, Value: []byte{0}},
		message.Option{ID: message.MaxAge, Value: []byte{0}},
	)
	request, serverExchange, err := server.UnprotectRequest(protectedRequest)
	if err != nil {
		t.Fatalf("UnprotectRequest with outer Class E options: %v", err)
	}
	if request.Options.HasOption(message.URIPath) || request.Options.HasOption(message.ContentFormat) {
		t.Fatalf("outer Class E options were not discarded: %v", request.Options)
	}
	if request.Options.HasOption(message.MaxAge) {
		t.Fatal("outer Max-Age was not discarded from the request")
	}

	protectedResponse, err := server.ProtectResponse(message.Message{
		Code: codes.Content,
		Options: message.Options{
			{ID: message.MaxAge, Value: []byte{42}},
		},
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	protectedResponse.Options = append(protectedResponse.Options,
		message.Option{ID: message.URIPath, Value: []byte("unexpected")},
		message.Option{ID: message.MaxAge, Value: []byte{0}},
	)
	plaintext, err := client.UnprotectResponse(protectedResponse, exchange)
	if err != nil {
		t.Fatalf("UnprotectResponse with outer Class E option: %v", err)
	}
	if plaintext.Options.HasOption(message.URIPath) {
		t.Fatalf("outer Class E option was not discarded: %v", plaintext.Options)
	}
	if maxAge, err := plaintext.Options.GetUint32(message.MaxAge); err != nil || maxAge != 42 {
		t.Fatalf("authenticated response Max-Age = %d, error = %v; want 42", maxAge, err)
	}
}

func TestUnprotectRejectsMalformedOrTamperedResponse(t *testing.T) {
	tests := map[string]func(*testing.T, *message.Message){
		"duplicate OSCORE option": func(_ *testing.T, response *message.Message) {
			response.Options = append(response.Options, message.Option{
				ID:    message.OSCORE,
				Value: []byte{},
			})
		},
		"response KID": func(t *testing.T, response *message.Message) {
			encoded, err := (OptionValue{SenderID: []byte{1}, SenderIDSet: true}).MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			response.Options = response.Options.Set(message.Option{ID: message.OSCORE, Value: encoded})
		},
		"tampered ciphertext": func(_ *testing.T, response *message.Message) {
			response.Payload = append([]byte(nil), response.Payload...)
			response.Payload[len(response.Payload)-1] ^= 0x80
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			client, server := pairedContexts(t, 0, 0)
			request, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
			if err != nil {
				t.Fatal(err)
			}
			_, serverExchange, err := server.UnprotectRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.ProtectResponse(
				message.Message{Code: codes.Content, Payload: []byte("response")},
				serverExchange,
			)
			if err != nil {
				t.Fatal(err)
			}
			mutated := response
			mutated.Options = CloneOptions(response.Options)
			mutate(t, &mutated)
			if _, err := client.UnprotectResponse(mutated, clientExchange); err == nil {
				t.Fatal("expected invalid response to fail")
			}
			if _, err := client.UnprotectResponse(response, clientExchange); err != nil {
				t.Fatalf("invalid response changed state or valid response failed: %v", err)
			}
		})
	}
}

func TestUnprotectResponseWithPartialIVRejectsReplay(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ProtectResponseWithPartialIV(
		message.Message{Code: codes.Content, Payload: []byte("response")},
		serverExchange,
	)
	if err != nil {
		t.Fatal(err)
	}
	tampered := response
	tampered.Payload = append([]byte(nil), response.Payload...)
	tampered.Payload[len(tampered.Payload)-1] ^= 0x01
	if _, err := client.UnprotectResponse(tampered, clientExchange); err == nil {
		t.Fatal("expected tampered response authentication to fail")
	}
	if _, err := client.UnprotectResponse(response, clientExchange); err != nil {
		t.Fatalf("failed authentication consumed response replay state: %v", err)
	}
	if _, err := client.UnprotectResponse(response, clientExchange); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed response error = %v, want ErrReplay", err)
	}
}

func TestQBlockDuplicatePartialIVRejectedWhenDeliveredConcurrently(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	request, clientExchange, err := client.ProtectRequest(message.Message{
		Code:  codes.GET,
		Token: message.Token{0x52},
		Options: message.Options{
			{ID: message.QBlock2, Value: []byte{0x0e}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ProtectResponseWithPartialIV(message.Message{
		Code:  codes.Content,
		Token: request.Token,
		Options: message.Options{
			{ID: message.QBlock2, Value: []byte{0x06}},
		},
		Payload: []byte("block"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unprotectErr := client.UnprotectResponse(response, clientExchange)
			errs <- unprotectErr
		}()
	}
	wg.Wait()
	close(errs)

	var accepted, rejected int
	for unprotectErr := range errs {
		switch {
		case unprotectErr == nil:
			accepted++
		case errors.Is(unprotectErr, ErrReplay):
			rejected++
		default:
			t.Fatalf("unexpected concurrent response error: %v", unprotectErr)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("duplicate Q-Block response: accepted=%d rejected=%d; want one of each", accepted, rejected)
	}
}

func optionValue(t *testing.T, options message.Options, id message.OptionID) []byte {
	t.Helper()
	first, end, err := options.Find(id)
	if err != nil || end-first != 1 {
		t.Fatalf("option %d count is invalid: range %d:%d, error %v", id, first, end, err)
	}
	return options[first].Value
}

func optionsEqual(first, second message.Options) bool {
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		if first[i].ID != second[i].ID || !bytes.Equal(first[i].Value, second[i].Value) {
			return false
		}
	}
	return true
}
