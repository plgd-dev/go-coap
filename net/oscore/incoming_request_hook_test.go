package oscore

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

// testEDHOCMessage3 stands in for EDHOC message_3, a CBOR byte string.
var testEDHOCMessage3 = []byte{0x43, 0x01, 0x02, 0x03}

// combinedRequest builds an RFC 9668 EDHOC + OSCORE request from a request
// protected with client.
func combinedRequest(t *testing.T, client *Context, request message.Message) (message.Message, Exchange) {
	t.Helper()
	protected, exchange, err := client.ProtectRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	protected.Options = protected.Options.Add(message.Option{ID: message.EDHOC})
	protected.Payload = append(append([]byte(nil), testEDHOCMessage3...), protected.Payload...)
	return protected, exchange
}

// edhocHook returns a hook that removes the EDHOC option and message_3 from a
// combined request, as RFC 9668 Section 3.3.1 Steps 6 and 7 do, and adds
// established.
func edhocHook(established *Context) IncomingRequestHook {
	return func(processor *Processor, request message.Message) (message.Message, error) {
		if !request.Options.HasOption(message.EDHOC) {
			return request, nil
		}
		if !bytes.HasPrefix(request.Payload, testEDHOCMessage3) {
			return message.Message{}, errors.New("malformed EDHOC + OSCORE payload")
		}
		if err := processor.AddContext(established); err != nil {
			return message.Message{}, err
		}
		request.Options = request.Options.Remove(message.EDHOC)
		request.Payload = request.Payload[len(testEDHOCMessage3):]
		return request, nil
	}
}

func TestProcessorIncomingRequestHookEstablishesContext(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		PlaintextPolicy:     PlaintextDisabled,
		IncomingRequestHook: edhocHook(server),
	})
	if err != nil {
		t.Fatalf("NewProcessor with a hook and no contexts: %v", err)
	}

	request, clientExchange := combinedRequest(t, client, message.Message{
		Code: codes.POST, Token: message.Token{1}, Payload: []byte("first request"),
	})
	plaintext, context, serverExchange, err := processor.ProcessIncomingRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if context != server || plaintext.Code != codes.POST || string(plaintext.Payload) != "first request" {
		t.Fatalf("combined request result = %+v, context %p, want context %p", plaintext, context, server)
	}
	if plaintext.Options.HasOption(message.EDHOC) {
		t.Fatal("EDHOC option reached the application request")
	}
	protectedResponse, err := server.ProtectResponse(message.Message{
		Code: codes.Changed, Token: plaintext.Token, Payload: []byte("first response"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.UnprotectResponse(protectedResponse, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != codes.Changed || string(response.Payload) != "first response" {
		t.Fatalf("response = %+v", response)
	}

	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET, Token: message.Token{2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, context, _, err = processor.ProcessIncomingRequest(protected); err != nil || context != server {
		t.Fatalf("request after establishment: context %p, error %v", context, err)
	}
}

func TestProcessorIncomingRequestHookOwnsOptionsCopy(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		PlaintextPolicy:     PlaintextDisabled,
		IncomingRequestHook: edhocHook(server),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := combinedRequest(t, client, message.Message{Code: codes.GET, Token: message.Token{1}})
	request.Options = request.Options.Add(message.Option{ID: message.Size1, Value: []byte{1}})
	options := CloneOptions(request.Options)
	if _, _, _, err := processor.ProcessIncomingRequest(request); err != nil {
		t.Fatal(err)
	}
	for i := range options {
		if request.Options[i].ID != options[i].ID || !bytes.Equal(request.Options[i].Value, options[i].Value) {
			t.Fatalf("caller option %d = %+v, want %+v", i, request.Options[i], options[i])
		}
	}
}

func TestProcessorRejectsUnprocessedEDHOCOption(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	single := func(t *testing.T) message.Message {
		request, _ := combinedRequest(t, client, message.Message{Code: codes.GET, Token: message.Token{1}})
		return request
	}
	repeated := func(t *testing.T) message.Message {
		request := single(t)
		request.Options = request.Options.Add(message.Option{ID: message.EDHOC})
		return request
	}
	tests := []struct {
		name    string
		hook    IncomingRequestHook
		request func(*testing.T) message.Message
	}{
		{name: "no hook", request: single},
		{
			name: "hook ignores EDHOC",
			hook: func(_ *Processor, request message.Message) (message.Message, error) {
				return request, nil
			},
			request: single,
		},
		{name: "repeated EDHOC option", hook: edhocHook(newTestContext(t, []byte{3}, []byte{4})), request: repeated},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor, err := NewProcessor(ProcessorConfig{
				Contexts:            []*Context{server},
				PlaintextPolicy:     PlaintextDisabled,
				IncomingRequestHook: test.hook,
			})
			if err != nil {
				t.Fatal(err)
			}
			plaintext, context, exchange, err := processor.ProcessIncomingRequest(test.request(t))
			if !errors.Is(err, ErrUnprocessedEDHOCOption) {
				t.Fatalf("ProcessIncomingRequest error = %v, want ErrUnprocessedEDHOCOption", err)
			}
			if plaintext.Code != codes.Empty || context != nil || len(exchange.RequestPartialIVBytes()) != 0 {
				t.Fatalf("rejected request result = %+v, context %p, exchange %+v", plaintext, context, exchange)
			}
			if got := ProcessingErrorCode(err); got != codes.BadOption {
				t.Fatalf("ProcessingErrorCode = %v, want %v", got, codes.BadOption)
			}
		})
	}
}

func TestProcessorIncomingRequestHookErrors(t *testing.T) {
	errEDHOC := errors.New("EDHOC message_3 verification failed")
	tests := []struct {
		name         string
		hookErr      error
		wantCode     codes.Code
		wantInternal bool
	}{
		{name: "peer error", hookErr: errEDHOC, wantCode: codes.BadRequest},
		{name: "unknown session", hookErr: ErrNoSecurityContext, wantCode: codes.Unauthorized},
		{name: "local fault", hookErr: ErrInternalProcessing, wantCode: codes.InternalServerError, wantInternal: true},
		{name: "AddContext failure", hookErr: ErrInvalidContext, wantCode: codes.InternalServerError, wantInternal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := pairedContexts(t, 0, 0)
			processor, err := NewProcessor(ProcessorConfig{
				PlaintextPolicy: PlaintextDisabled,
				IncomingRequestHook: func(*Processor, message.Message) (message.Message, error) {
					return message.Message{}, test.hookErr
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := combinedRequest(t, client, message.Message{Code: codes.GET, Token: message.Token{1}})
			_, context, _, err := processor.ProcessIncomingRequest(request)
			if !errors.Is(err, test.hookErr) || context != nil {
				t.Fatalf("ProcessIncomingRequest = context %p, error %v, want %v", context, err, test.hookErr)
			}
			if got := ProcessingErrorCode(err); got != test.wantCode {
				t.Fatalf("ProcessingErrorCode = %v, want %v", got, test.wantCode)
			}
			if IsInternalProcessingError(err) != test.wantInternal {
				t.Fatalf("IsInternalProcessingError = %v, want %v", !test.wantInternal, test.wantInternal)
			}
		})
	}
}

func TestProcessorIncomingRequestHookSkipsUnprotectedRequests(t *testing.T) {
	_, server := pairedContexts(t, 0, 0)
	var calls atomic.Int32
	hook := func(_ *Processor, request message.Message) (message.Message, error) {
		calls.Add(1)
		return request, nil
	}
	for _, policy := range []PlaintextPolicy{PlaintextAllowed, PlaintextOnly} {
		processor, err := NewProcessor(ProcessorConfig{
			Contexts:            []*Context{server},
			PlaintextPolicy:     policy,
			IncomingRequestHook: hook,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := processor.ProcessIncomingRequest(message.Message{Code: codes.GET}); err != nil {
			t.Fatalf("policy %d plaintext request: %v", policy, err)
		}
	}
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:            []*Context{server},
		PlaintextPolicy:     PlaintextOnly,
		IncomingRequestHook: hook,
	})
	if err != nil {
		t.Fatal(err)
	}
	optionValue, err := (OptionValue{PartialIV: []byte{1}, SenderID: []byte{0}, SenderIDSet: true}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, _, err := processor.ProcessIncomingRequest(message.Message{
		Code: codes.POST, Options: message.Options{{ID: message.OSCORE, Value: optionValue}},
	})
	if !errors.Is(err, ErrOSCOREDisabled) || plaintext.Code != codes.Empty {
		t.Fatalf("PlaintextOnly protected request error = %v, want ErrOSCOREDisabled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("hook called %d times for requests the policy decides", got)
	}
}

func TestUnprotectResponseDropsOuterEDHOCOption(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	protected, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET, Token: message.Token{1}})
	if err != nil {
		t.Fatal(err)
	}
	_, serverExchange, err := server.UnprotectRequest(protected)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.ProtectResponse(message.Message{Code: codes.Content, Token: message.Token{1}}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	response.Options = response.Options.Add(message.Option{ID: message.EDHOC})
	opened, err := client.UnprotectResponse(response, clientExchange)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Options.HasOption(message.EDHOC) {
		t.Fatal("unauthenticated outer EDHOC option reached the response")
	}
}

func TestProcessorIncomingRequestHookSkipsInvalidOSCOREOption(t *testing.T) {
	_, server := pairedContexts(t, 0, 0)
	var calls atomic.Int32
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:        []*Context{server},
		PlaintextPolicy: PlaintextDisabled,
		IncomingRequestHook: func(_ *Processor, request message.Message) (message.Message, error) {
			calls.Add(1)
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	withKID, err := (OptionValue{PartialIV: []byte{1}, SenderID: []byte{0}, SenderIDSet: true}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	withoutKID, err := (OptionValue{PartialIV: []byte{1}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for name, options := range map[string]message.Options{
		"duplicate OSCORE option": {{ID: message.OSCORE, Value: withKID}, {ID: message.OSCORE, Value: withKID}, {ID: message.EDHOC}},
		"no KID":                  {{ID: message.OSCORE, Value: withoutKID}, {ID: message.EDHOC}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := processor.ProcessIncomingRequest(message.Message{Code: codes.POST, Options: options})
			if !errors.Is(err, ErrInvalidOSCOREMessage) {
				t.Fatalf("ProcessIncomingRequest error = %v, want ErrInvalidOSCOREMessage", err)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("hook called %d times for requests with an invalid OSCORE option", got)
	}
}
