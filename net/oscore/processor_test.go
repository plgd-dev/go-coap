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

func TestProcessorSelectsOutgoingContext(t *testing.T) {
	const masterSecret = "shared master secret"
	clientA, err := NewContext(ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte{0x10},
		RecipientID:  []byte{0x20},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverA, err := NewContext(ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte{0x20},
		RecipientID:  []byte{0x10},
	})
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := NewContext(ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte{0x11},
		RecipientID:  []byte{0x21},
	})
	if err != nil {
		t.Fatal(err)
	}
	serverB, err := NewContext(ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte{0x21},
		RecipientID:  []byte{0x11},
	})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(ProcessorConfig{
		Contexts: []*Context{clientA, clientB},
		OutgoingContextSelector: func(request message.Message, contexts []*Context) (*Context, error) {
			if len(contexts) != 2 {
				t.Fatalf("selector received %d contexts, want 2", len(contexts))
			}
			host, hostErr := request.Options.GetString(message.URIHost)
			if hostErr != nil {
				return nil, hostErr
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
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		host        string
		wantContext *Context
		server      *Context
	}{
		{host: "peer-a.example", wantContext: clientA, server: serverA},
		{host: "peer-b.example", wantContext: clientB, server: serverB},
	} {
		t.Run(tt.host, func(t *testing.T) {
			request := message.Message{
				Code: codes.GET,
				Options: message.Options{
					{ID: message.URIHost, Value: []byte(tt.host)},
					{ID: message.URIPath, Value: []byte("resource")},
				},
				Payload: []byte("original"),
			}
			protected, selected, _, err := processor.ProtectRequestWithContext(request)
			if err != nil {
				t.Fatal(err)
			}
			if selected != tt.wantContext {
				t.Fatalf("selected context %p, want %p", selected, tt.wantContext)
			}
			if request.Code != codes.GET || string(request.Options[0].Value) != tt.host ||
				string(request.Payload) != "original" {
				t.Fatalf("selector mutated caller request: %+v", request)
			}
			opened, _, err := tt.server.UnprotectRequest(protected)
			if err != nil {
				t.Fatal(err)
			}
			if opened.Code != codes.GET {
				t.Fatalf("opened request code = %v, want GET", opened.Code)
			}
		})
	}
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
	foreign, err := NewContext(ContextConfig{MasterSecret: []byte("foreign context")})
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
