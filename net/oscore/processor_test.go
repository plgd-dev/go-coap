package oscore

import (
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func TestProcessorSupportsProtectedAndPlaintextRequests(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:         []*Context{server},
		AllowUnprotected: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	plaintext := message.Message{Code: codes.GET, Token: message.Token{1}}
	gotPlaintext, context, exchange, err := processor.ProcessIncomingRequest(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if context != nil || len(exchange.RequestPartialIV) != 0 || gotPlaintext.Code != plaintext.Code {
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
	if context != server || gotProtected.Code != codes.GET || len(exchange.RequestPartialIV) == 0 ||
		string(exchange.RequestPartialIV) != string(clientExchange.RequestPartialIV) {
		t.Fatalf("protected result = %+v, context %p, exchange %+v", gotProtected, context, exchange)
	}
}

func TestProcessorRequiresOSCOREOrRejectsPlaintext(t *testing.T) {
	_, server := pairedContexts(t, 0, 0)
	for _, cfg := range []ProcessorConfig{
		{Contexts: []*Context{server}, RequireOSCORE: true},
		{Contexts: []*Context{server}},
	} {
		processor, err := NewProcessor(cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = processor.ProcessIncomingRequest(message.Message{Code: codes.GET})
		if cfg.RequireOSCORE && !errors.Is(err, ErrOSCORERequired) {
			t.Fatalf("required-OSCORE plaintext error = %v", err)
		}
		if !cfg.RequireOSCORE && !errors.Is(err, ErrPlaintextDisabled) {
			t.Fatalf("strict-policy plaintext error = %v", err)
		}
	}
}

func TestProcessorNeverFallsBackAfterOSCOREFailure(t *testing.T) {
	client, server := pairedContexts(t, 0, 0)
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:         []*Context{server},
		AllowUnprotected: true,
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
		MasterSecret: masterSecret,
		IDContext:    []byte{0xa1},
		SenderID:     []byte{0x10},
		RecipientID:  []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewContext(ContextConfig{
		MasterSecret: masterSecret,
		IDContext:    []byte{0xa1},
		SenderID:     []byte{0x20},
		RecipientID:  []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherServer, err := NewContext(ContextConfig{
		MasterSecret: masterSecret,
		IDContext:    []byte{0xa2},
		SenderID:     []byte{0x20},
		RecipientID:  []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(ProcessorConfig{
		Contexts:         []*Context{server, otherServer},
		AllowUnprotected: true,
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
		MasterSecret: masterSecret,
		IDContext:    []byte{0xa3},
		SenderID:     []byte{0x20},
		RecipientID:  []byte{0x10},
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
	if _, err := NewProcessor(ProcessorConfig{AllowUnprotected: true, RequireOSCORE: true}); err == nil {
		t.Fatal("expected conflicting plaintext policy to fail")
	}
	if _, err := NewProcessor(ProcessorConfig{RequireOSCORE: true}); err == nil {
		t.Fatal("expected required OSCORE without contexts to fail")
	}
}

func TestProcessorRejectsDuplicateSenderSecurityContext(t *testing.T) {
	newContext := func(recipientID, masterSalt, idContext []byte) *Context {
		t.Helper()
		context, err := NewContext(ContextConfig{
			MasterSecret: []byte("duplicate sender context test"),
			MasterSalt:   masterSalt,
			IDContext:    idContext,
			SenderID:     []byte{1},
			RecipientID:  recipientID,
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

	differentSalt := newContext([]byte{6}, []byte{7}, []byte{4})
	if err := processor.AddContext(differentSalt); err != nil {
		t.Fatalf("AddContext context with different Master Salt: %v", err)
	}
	differentIDContext := newContext([]byte{8}, []byte{3}, []byte{9})
	if err := processor.AddContext(differentIDContext); err != nil {
		t.Fatalf("AddContext context with different ID Context: %v", err)
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
		MasterSecret: []byte("shared master secret"),
		SenderID:     senderID,
		RecipientID:  recipientID,
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
		MasterSecret: []byte("shared master secret"),
		SenderID:     []byte{0x10},
		RecipientID:  []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewContext(ContextConfig{
		MasterSecret: []byte("shared master secret"),
		SenderID:     []byte{0x11},
		RecipientID:  []byte{0x21},
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewContext(ContextConfig{
		MasterSecret: []byte("foreign context"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
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

func TestProcessorRequiresSelectorForMultipleOutgoingContexts(t *testing.T) {
	clientA, err := NewContext(ContextConfig{
		MasterSecret: []byte("shared master secret"),
		SenderID:     []byte{0x10},
		RecipientID:  []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewContext(ContextConfig{
		MasterSecret: []byte("shared master secret"),
		SenderID:     []byte{0x11},
		RecipientID:  []byte{0x21},
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
