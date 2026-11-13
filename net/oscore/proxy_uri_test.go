package oscore

import (
	"bytes"
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

func TestParseProxyURI(t *testing.T) {
	target, err := ParseProxyURI("coap://[2001:db8::1]:5683/a%2Fb/c/?q=a+b&x=%2F")
	if err != nil {
		t.Fatal(err)
	}
	if target.Scheme != "coap" || target.Host != "2001:db8::1" || target.Port != "5683" {
		t.Fatalf("target authority = %+v", target)
	}
	if target.AuthorityURI() != "coap://[2001:db8::1]" || target.Address() != "[2001:db8::1]:5683" {
		t.Fatalf("target address = %q / %q", target.AuthorityURI(), target.Address())
	}
	if len(target.Path) != 3 || target.Path[0] != "a/b" || target.Path[1] != "c" || target.Path[2] != "" {
		t.Fatalf("target path = %#v", target.Path)
	}
	if len(target.Query) != 2 || target.Query[0] != "q=a+b" || target.Query[1] != "x=/" {
		t.Fatalf("target query = %#v", target.Query)
	}
}

func TestProtectRequestDecomposesProxyURI(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, _, err := ctx.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://origin.example:5683/sensors/temp?q=1"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := protected.Options.GetString(message.ProxyURI)
	if err != nil {
		t.Fatal(err)
	}
	if authority != "coap://origin.example" {
		t.Fatalf("outer Proxy-Uri = %q", authority)
	}
	if protected.Options.HasOption(message.URIPath) || protected.Options.HasOption(message.URIQuery) {
		t.Fatal("URI path or query leaked into the outer options")
	}
}

func TestParseProxyURIRejectsInvalidTargets(t *testing.T) {
	for _, value := range []string{
		"coap://user@host/path",
		"coap://host:0/path",
		"coap://host:70000/path",
		"coap://host/%zz",
		"coap://host/path#",
		"https://host/path",
		"/relative/path",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseProxyURI(value)
			if !errors.Is(err, ErrInvalidOSCOREMessage) {
				t.Fatalf("ParseProxyURI error = %v", err)
			}
		})
	}
}

func TestProtectRequestRejectsProxyURIWithSeparateURIOptions(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ctx.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{
			{ID: message.URIPath, Value: []byte("path")},
			{ID: message.ProxyURI, Value: []byte("coap://origin.example/path")},
		},
	})
	if !errors.Is(err, ErrInvalidOSCOREMessage) {
		t.Fatalf("ProtectRequest error = %v", err)
	}
}

func TestProtectProxyRequestKeepsAuthorityOutsideCiphertext(t *testing.T) {
	ctx, err := NewContext(ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	protected, _, err := ctx.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://origin.example/a%2Fb?q=one+two"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !protected.Options.HasOption(message.OSCORE) || len(protected.Payload) == 0 {
		t.Fatalf("protected message missing OSCORE data: %+v", protected)
	}
	if bytes.Contains(protected.Payload, []byte("origin.example")) ||
		bytes.Contains(protected.Payload, []byte("a/b")) {
		t.Fatalf("protected payload unexpectedly contains URI text: %x", protected.Payload)
	}
}
