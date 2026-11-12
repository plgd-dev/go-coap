package oscore

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func TestProcessingErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "unknown context", err: ErrNoSecurityContext, want: codes.Unauthorized},
		{name: "replay", err: ErrReplay, want: codes.Unauthorized},
		{name: "malformed option", err: fmt.Errorf("%w: malformed", ErrInvalidOSCOREOption), want: codes.BadOption},
		{name: "decrypt failure", err: errors.New("authentication failed"), want: codes.BadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ProcessingErrorCode(test.err); got != test.want {
				t.Fatalf("ProcessingErrorCode(%v) = %v, want %v", test.err, got, test.want)
			}
			if !IsUnprotectedProcessingErrorCode(test.want) {
				t.Fatalf("response code %v is not recognized as an unprotected processing error", test.want)
			}
		})
	}
}

func TestProcessorProcessingErrorClassification(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{Contexts: []*Context{server}})
	if err != nil {
		t.Fatal(err)
	}

	malformed := message.Message{
		Code:    codes.GET,
		Options: message.Options{{ID: message.OSCORE, Value: []byte{0x09}}},
	}
	if _, _, _, processErr := processor.ProcessIncomingRequest(malformed); !errors.Is(processErr, ErrInvalidOSCOREOption) ||
		ProcessingErrorCode(processErr) != codes.BadOption {
		t.Fatalf("malformed OSCORE option classification error = %v, code = %v", processErr, ProcessingErrorCode(processErr))
	}

	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET, Token: message.Token{1}})
	if err != nil {
		t.Fatal(err)
	}
	invalidCiphertext := protected
	invalidCiphertext.Payload = append([]byte(nil), protected.Payload...)
	invalidCiphertext.Payload[len(invalidCiphertext.Payload)-1] ^= 0xff
	if _, _, _, processErr := processor.ProcessIncomingRequest(invalidCiphertext); processErr == nil ||
		ProcessingErrorCode(processErr) != codes.BadRequest {
		t.Fatalf("invalid ciphertext classification error = %v, code = %v", processErr, ProcessingErrorCode(processErr))
	}

	if _, _, _, processErr := processor.ProcessIncomingRequest(protected); processErr != nil {
		t.Fatalf("process valid request: %v", processErr)
	}
	if _, _, _, processErr := processor.ProcessIncomingRequest(protected); !errors.Is(processErr, ErrReplay) ||
		ProcessingErrorCode(processErr) != codes.Unauthorized {
		t.Fatalf("replayed request classification error = %v, code = %v", processErr, ProcessingErrorCode(processErr))
	}
}

func TestProcessorSupportsProtectedAndPlaintextRequests(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:        []*Context{server},
		PlaintextPolicy: PlaintextAllowed,
	})
	if err != nil {
		t.Fatal(err)
	}

	plaintext := message.Message{Code: codes.GET, Token: message.Token{1}}
	gotPlaintext, context, exchange, err := processor.ProcessIncomingRequest(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if context != nil || len(exchange.RequestPartialIVBytes()) != 0 || gotPlaintext.Code != plaintext.Code {
		t.Fatalf("plaintext result = %+v, context %p, exchange %+v", gotPlaintext, context, exchange)
	}

	protected, clientExchange, err := client.ProtectRequest(message.Message{Code: codes.GET, Token: message.Token{2}})
	if err != nil {
		t.Fatal(err)
	}
	gotProtected, context, exchange, err := processor.ProcessIncomingRequest(protected)
	if err != nil {
		t.Fatal(err)
	}
	if context != server || gotProtected.Code != codes.GET || len(exchange.RequestPartialIVBytes()) == 0 ||
		string(exchange.RequestPartialIVBytes()) != string(clientExchange.RequestPartialIVBytes()) {
		t.Fatalf("protected result = %+v, context %p, exchange %+v", gotProtected, context, exchange)
	}
}

func TestProcessorRequiresOSCOREOrRejectsPlaintext(t *testing.T) {
	_, server := pairedContexts(t, 0, 0)
	for _, cfg := range []ProcessorConfig{
		{Contexts: []*Context{server}, PlaintextPolicy: PlaintextDisabled},
	} {
		processor, err := NewProcessor(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = processor.ProcessIncomingRequest(message.Message{Code: codes.GET})
		if !errors.Is(err, ErrPlaintextDisabled) {
			t.Fatalf("strict-policy plaintext error = %v", err)
		}
	}
	if _, err := NewProcessor(ProcessorConfig{PlaintextPolicy: PlaintextDisabled}); err == nil {
		t.Fatal("expected required OSCORE without contexts to fail")
	}
}

func TestProcessorNeverFallsBackAfterOSCOREFailure(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:        []*Context{server},
		PlaintextPolicy: PlaintextAllowed,
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	protected.Payload = append([]byte(nil), protected.Payload...)
	protected.Payload[len(protected.Payload)-1] ^= 0x01
	if _, _, _, err := processor.ProcessIncomingRequest(protected); err == nil {
		t.Fatal("expected authentication failure")
	}
}

func TestProcessorSelectsContextByKIDAndIDContext(t *testing.T) {
	masterSecret := []byte{1, 2, 3}
	client, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{0xa1},
		SenderID:              []byte{0x10},
		RecipientID:           []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{0xa1},
		SenderID:              []byte{0x20},
		RecipientID:           []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherServer, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{0xa2},
		SenderID:              []byte{0x20},
		RecipientID:           []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:        []*Context{server, otherServer},
		PlaintextPolicy: PlaintextAllowed,
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	_, selected, _, err := processor.ProcessIncomingRequest(protected)
	if err != nil {
		t.Fatal(err)
	}
	if selected != server {
		t.Fatalf("selected context %p, want %p", selected, server)
	}

	wrongContext, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          masterSecret,
		IDContext:             []byte{0xa3},
		SenderID:              []byte{0x20},
		RecipientID:           []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	noMatch, err := NewProcessor(ProcessorConfig{Contexts: []*Context{wrongContext}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := noMatch.ProcessIncomingRequest(protected); !errors.Is(err, ErrNoSecurityContext) {
		t.Fatalf("context mismatch error = %v, want ErrNoSecurityContext", err)
	}
}

func TestProcessorRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewProcessor(ProcessorConfig{PlaintextPolicy: PlaintextPolicy(99)}); err == nil {
		t.Fatal("expected invalid plaintext policy to fail")
	}
}

func TestProcessorExplicitPlaintextPolicies(t *testing.T) {
	plaintext := message.Message{Code: codes.GET}
	for _, test := range []struct {
		name         string
		policy       PlaintextPolicy
		plaintextErr error
		protectedErr error
	}{
		{name: "disabled", policy: PlaintextDisabled, plaintextErr: ErrPlaintextDisabled},
		{name: "allowed", policy: PlaintextAllowed},
		{name: "only", policy: PlaintextOnly, protectedErr: ErrOSCOREDisabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := pairedContexts(t, 0, 0)
			protected, _, err := client.ProtectRequest(message.Message{Code: codes.GET})
			if err != nil {
				t.Fatal(err)
			}
			processor, err := NewProcessor(ProcessorConfig{
				Contexts: []*Context{server}, PlaintextPolicy: test.policy,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, plaintextErr := processor.ProcessIncomingRequest(plaintext)
			if !errors.Is(plaintextErr, test.plaintextErr) {
				t.Fatalf("plaintext error = %v, want %v", plaintextErr, test.plaintextErr)
			}
			_, _, _, protectedErr := processor.ProcessIncomingRequest(protected)
			if !errors.Is(protectedErr, test.protectedErr) {
				t.Fatalf("protected error = %v, want %v", protectedErr, test.protectedErr)
			}
		})
	}
}

func TestProcessorRemoveContextPreservesInFlightExchange(t *testing.T) {
	clientContext, serverContext := pairedContexts(t, 0, 0)
	clientProcessor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{clientContext}, PlaintextPolicy: PlaintextDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverProcessor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{serverContext}, PlaintextPolicy: PlaintextDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	protectedRequest, exchange, err := clientProcessor.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, serverExchange, err := serverProcessor.ProcessIncomingRequest(protectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	removeErr := clientProcessor.RemoveContext(clientContext.RecipientID(), nil)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	removeErr = clientProcessor.RemoveContext(clientContext.RecipientID(), nil)
	if !errors.Is(removeErr, ErrNoSecurityContext) {
		t.Fatalf("duplicate removal error = %v, want ErrNoSecurityContext", removeErr)
	}
	_, _, protectErr := clientProcessor.ProtectRequest(message.Message{Code: codes.GET})
	if !errors.Is(protectErr, ErrNoSecurityContext) {
		t.Fatalf("outgoing request after removal error = %v, want ErrNoSecurityContext", protectErr)
	}

	protectedResponse, err := serverContext.ProtectResponse(message.Message{
		Code: codes.Content, Token: request.Token, Payload: []byte("in flight"),
	}, serverExchange)
	if err != nil {
		t.Fatal(err)
	}
	response, err := clientProcessor.UnprotectResponse(protectedResponse, clientContext, exchange)
	if err != nil {
		t.Fatalf("in-flight response after removal failed: %v", err)
	}
	if string(response.Payload) != "in flight" {
		t.Fatalf("response payload = %q, want in flight", response.Payload)
	}

	removeErr = serverProcessor.RemoveContext(serverContext.RecipientID(), nil)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	_, selectedContext, receivedExchange, processErr := serverProcessor.ProcessIncomingRequest(protectedRequest)
	if !errors.Is(processErr, ErrNoSecurityContext) {
		t.Fatalf("incoming request after removal error = %v, want ErrNoSecurityContext", processErr)
	}
	if selectedContext != nil || receivedExchange.responseState != nil {
		t.Fatal("request after context removal returned authenticated state")
	}
}

func TestProcessorRemoveContextClearsOutgoingBackingArray(t *testing.T) {
	context := newTestContext(t, []byte{0x10}, []byte{0x20})
	processor, err := NewProcessor(ProcessorConfig{Contexts: []*Context{context}})
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.RemoveContext(context.RecipientID(), nil); err != nil {
		t.Fatal(err)
	}
	if len(processor.outgoingContexts) != 0 {
		t.Fatalf("outgoing context count = %d, want 0", len(processor.outgoingContexts))
	}
	backing := processor.outgoingContexts[:cap(processor.outgoingContexts)]
	if len(backing) != 0 && backing[0] != nil {
		t.Fatal("removed context remains in outgoingContexts backing array")
	}
}

func TestProcessorRemoveContextIsScopedToIDContext(t *testing.T) {
	newContext := func(senderID, recipientID, idContext byte) *Context {
		t.Helper()
		context, err := NewContext(ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          []byte("context removal scope"),
			IDContext:             []byte{idContext},
			SenderID:              []byte{senderID},
			RecipientID:           []byte{recipientID},
		})
		if err != nil {
			t.Fatal(err)
		}
		return context
	}
	clientA, serverA := newContext(1, 2, 0xa1), newContext(2, 1, 0xa1)
	clientB, serverB := newContext(1, 2, 0xa2), newContext(2, 1, 0xa2)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{serverA, serverB}, PlaintextPolicy: PlaintextDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestA, _, err := clientA.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	requestB, _, err := clientB.ProtectRequest(message.Message{Code: codes.GET})
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.RemoveContext(serverA.RecipientID(), []byte{0xa1}); err != nil {
		t.Fatal(err)
	}
	_, selectedA, exchangeA, processErr := processor.ProcessIncomingRequest(requestA)
	if !errors.Is(processErr, ErrNoSecurityContext) {
		t.Fatalf("removed context request error = %v, want ErrNoSecurityContext", processErr)
	}
	if selectedA != nil || exchangeA.responseState != nil {
		t.Fatal("removed context returned authenticated state")
	}
	_, selectedB, _, processErr := processor.ProcessIncomingRequest(requestB)
	if processErr != nil || selectedB != serverB {
		t.Fatalf("other context after removal = %p, error %v; want %p, nil", selectedB, processErr, serverB)
	}
}

func TestProcessorContextLifecycleConcurrentWithProcessing(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{server}, PlaintextPolicy: PlaintextDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]message.Message, 32)
	for i := range requests {
		protected, _, protectErr := client.ProtectRequest(message.Message{
			Code: codes.GET, Token: message.Token{byte(i + 1)},
		})
		if protectErr != nil {
			t.Fatal(protectErr)
		}
		requests[i] = protected
	}

	errs := make(chan error, len(requests)+128)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		processRequestsDuringContextLifecycle(processor, server, requests, errs)
	}()
	go func() {
		defer wg.Done()
		mutateContextDuringProcessing(processor, server, errs)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent context lifecycle error: %v", err)
	}
}

func processRequestsDuringContextLifecycle(
	processor *Processor,
	server *Context,
	requests []message.Message,
	errs chan<- error,
) {
	for _, request := range requests {
		plaintext, selectedContext, exchange, err := processor.ProcessIncomingRequest(request)
		switch {
		case err != nil && !errors.Is(err, ErrNoSecurityContext):
			errs <- err
		case err == nil &&
			(plaintext.Code != codes.GET || selectedContext != server || len(exchange.RequestPartialIVBytes()) == 0):
			errs <- ErrInvalidContext
		}
	}
}

func mutateContextDuringProcessing(processor *Processor, server *Context, errs chan<- error) {
	for range 64 {
		if err := processor.AddContext(server); err != nil && !errors.Is(err, ErrInvalidContext) {
			errs <- err
		}
		if err := processor.RemoveContext(server.RecipientID(), nil); err != nil &&
			!errors.Is(err, ErrNoSecurityContext) {
			errs <- err
		}
	}
}

func TestProcessorRejectsDuplicateSenderSecurityContext(t *testing.T) {
	newContext := func(recipientID, masterSalt, idContext []byte) *Context {
		t.Helper()
		context, err := NewContext(ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          []byte("duplicate sender context test"),
			MasterSalt:            masterSalt,
			IDContext:             idContext,
			SenderID:              []byte{1},
			RecipientID:           recipientID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return context
	}

	first := newContext([]byte{2}, []byte{3}, []byte{4})
	duplicate := newContext([]byte{5}, []byte{3}, []byte{4})
	if _, err := NewProcessor(ProcessorConfig{Contexts: []*Context{first, duplicate}}); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("duplicate sender security context error = %v, want %v", err, ErrInvalidContext)
	}

	processor, err := NewProcessor(ProcessorConfig{Contexts: []*Context{first}})
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.AddContext(duplicate); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("AddContext duplicate sender security context error = %v, want %v", err, ErrInvalidContext)
	}
	idContext, idContextSet := first.IDContext()
	if !idContextSet {
		t.Fatal("test context has no ID Context")
	}
	if err := processor.RemoveContext(first.RecipientID(), idContext); err != nil {
		t.Fatal(err)
	}
	if err := processor.AddContext(duplicate); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("AddContext retired sender security context error = %v, want %v", err, ErrInvalidContext)
	}
	if err := processor.AddContext(first); err != nil {
		t.Fatalf("re-add exact Context instance: %v", err)
	}

	differentSalt := newContext([]byte{6}, []byte{7}, []byte{4})
	if err := processor.AddContext(differentSalt); err != nil {
		t.Fatalf("AddContext context with different Master Salt: %v", err)
	}
	differentIDContext := newContext([]byte{8}, []byte{3}, []byte{9})
	if err := processor.AddContext(differentIDContext); err != nil {
		t.Fatalf("AddContext context with different ID Context: %v", err)
	}
}

func TestContextShallowCopySharesSenderSequenceState(t *testing.T) {
	original, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shallow copy sender sequence test"),
		MasterSalt:            []byte{1, 2, 3},
		SenderID:              []byte{1},
		RecipientID:           []byte{2},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Exercise a shallow copy without bypassing go vet's copylocks check.
	copiedValue := reflect.New(reflect.TypeOf(original).Elem())
	copiedValue.Elem().Set(reflect.ValueOf(original).Elem())
	copied := copiedValue.Interface().(*Context)

	processor, err := NewProcessor(ProcessorConfig{Contexts: []*Context{original}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := original.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, encodePartialIV(0)) {
		t.Fatalf("original Partial IV = %x, want 00", first)
	}
	idContext, _ := original.IDContext()
	if removeErr := processor.RemoveContext(original.RecipientID(), idContext); removeErr != nil {
		t.Fatal(removeErr)
	}
	if addErr := processor.AddContext(copied); addErr != nil {
		t.Fatalf("register shallow copy sharing sender state: %v", addErr)
	}
	second, err := copied.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, encodePartialIV(1)) {
		t.Fatalf("copied Context Partial IV = %x, want 01", second)
	}
	if removeErr := processor.RemoveContext(copied.RecipientID(), idContext); removeErr != nil {
		t.Fatal(removeErr)
	}
	if addErr := processor.AddContext(original); addErr != nil {
		t.Fatalf("re-register original Context: %v", addErr)
	}
	third, err := original.NextPartialIV()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(third, encodePartialIV(2)) {
		t.Fatalf("re-registered original Partial IV = %x, want 02", third)
	}
}

func TestProcessorSelectsOutgoingContext(t *testing.T) {
	clientA := newTestContext(t, []byte{0x10}, []byte{0x20})
	serverA := newTestContext(t, []byte{0x20}, []byte{0x10})
	clientB := newTestContext(t, []byte{0x11}, []byte{0x21})
	serverB := newTestContext(t, []byte{0x21}, []byte{0x11})
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:                []*Context{clientA, clientB},
		OutgoingContextSelector: outgoingContextSelector(t, clientA, clientB),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertOutgoingContextSelection(t, processor, "peer-a.example", clientA, serverA)
	assertOutgoingContextSelection(t, processor, "peer-b.example", clientB, serverB)
}

func newTestContext(t *testing.T, senderID, recipientID []byte) *Context {
	t.Helper()
	context, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shared master secret"),
		SenderID:              senderID,
		RecipientID:           recipientID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return context
}

func outgoingContextSelector(t *testing.T, clientA, clientB *Context) OutgoingContextSelector {
	t.Helper()
	return func(request message.Message, contexts []*Context) (*Context, error) {
		if len(contexts) != 2 {
			t.Fatalf("selector received %d contexts, want 2", len(contexts))
		}
		host, err := request.Options.GetString(message.URIHost)
		if err != nil {
			return nil, err
		}
		switch host {
		case "peer-a.example":
			request.Code = codes.PUT
			request.Options[0].Value[0] = 'x'
			request.Payload = append(request.Payload, 0xff)
			return clientA, nil
		case "peer-b.example":
			return clientB, nil
		default:
			return nil, ErrNoSecurityContext
		}
	}
}

func assertOutgoingContextSelection(t *testing.T, processor *Processor, host string, wantContext, server *Context) {
	t.Helper()
	t.Run(host, func(t *testing.T) {
		request := message.Message{
			Code: codes.GET,
			Options: message.Options{
				{ID: message.URIHost, Value: []byte(host)},
				{ID: message.URIPath, Value: []byte("resource")},
			},
			Payload: []byte("original"),
		}
		protected, selected, _, err := processor.ProtectRequestWithContext(request)
		if err != nil {
			t.Fatal(err)
		}
		if selected != wantContext {
			t.Fatalf("selected context %p, want %p", selected, wantContext)
		}
		if request.Code != codes.GET || string(request.Options[0].Value) != host ||
			string(request.Payload) != "original" {
			t.Fatalf("selector mutated caller request: %+v", request)
		}
		opened, _, err := server.UnprotectRequest(protected)
		if err != nil {
			t.Fatal(err)
		}
		if opened.Code != codes.GET {
			t.Fatalf("opened request code = %v, want GET", opened.Code)
		}
	})
}

func TestProcessorOutgoingSelectorRejectsInvalidSelection(t *testing.T) {
	clientA, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shared master secret"),
		SenderID:              []byte{0x10},
		RecipientID:           []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shared master secret"),
		SenderID:              []byte{0x11},
		RecipientID:           []byte{0x21},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("foreign context"),
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	selectorErr := errors.New("no route for peer")
	tests := []struct {
		name     string
		selector OutgoingContextSelector
		wantErr  error
	}{
		{
			name: "selector error",
			selector: func(message.Message, []*Context) (*Context, error) {
				return nil, selectorErr
			},
			wantErr: selectorErr,
		},
		{
			name: "nil selection",
			selector: func(message.Message, []*Context) (*Context, error) {
				return nil, nil //nolint:nilnil // Verify that a nil successful selection maps to ErrNoSecurityContext.
			},
			wantErr: ErrNoSecurityContext,
		},
		{
			name: "unconfigured context",
			selector: func(message.Message, []*Context) (*Context, error) {
				return foreign, nil
			},
			wantErr: ErrNoSecurityContext,
		},
		{
			name: "mutated context snapshot",
			selector: func(_ message.Message, contexts []*Context) (*Context, error) {
				contexts[0] = foreign
				return foreign, nil
			},
			wantErr: ErrNoSecurityContext,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor, err := NewProcessor(ProcessorConfig{
				Contexts:                []*Context{clientA, clientB},
				OutgoingContextSelector: tt.selector,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := processor.ProtectRequest(message.Message{Code: codes.GET}); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ProtectRequest error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestProcessorOutgoingSelectorConcurrentContextChanges(t *testing.T) {
	clientA := newTestContext(t, []byte{0x10}, []byte{0x20})
	clientB := newTestContext(t, []byte{0x11}, []byte{0x21})
	changing := newTestContext(t, []byte{0x12}, []byte{0x22})
	processor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{clientA, clientB},
		OutgoingContextSelector: func(_ message.Message, contexts []*Context) (*Context, error) {
			if len(contexts) < 2 {
				return nil, ErrNoSecurityContext
			}
			for _, context := range contexts {
				if context == nil || len(context.SenderID()) == 0 {
					return nil, ErrInvalidContext
				}
			}
			return contexts[0], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 256)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 128 {
			_, _, protectErr := processor.ProtectRequest(message.Message{
				Code: codes.GET, Token: message.Token{byte(i + 1)},
			})
			if protectErr != nil {
				errs <- protectErr
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 128 {
			if addErr := processor.AddContext(changing); addErr != nil {
				errs <- addErr
				continue
			}
			if removeErr := processor.RemoveContext(changing.RecipientID(), nil); removeErr != nil {
				errs <- removeErr
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent selection/context change error: %v", err)
	}
}

func TestProcessorRequiresSelectorForMultipleOutgoingContexts(t *testing.T) {
	clientA, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shared master secret"),
		SenderID:              []byte{0x10},
		RecipientID:           []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewContext(ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("shared master secret"),
		SenderID:              []byte{0x11},
		RecipientID:           []byte{0x21},
	})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(ProcessorConfig{Contexts: []*Context{clientA, clientB}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := processor.ProtectRequest(message.Message{Code: codes.GET}); !errors.Is(err, ErrAmbiguousContext) {
		t.Fatalf("ProtectRequest error = %v, want ErrAmbiguousContext", err)
	}
}
