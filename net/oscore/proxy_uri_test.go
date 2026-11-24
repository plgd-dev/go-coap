package oscore

import (
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

const testOriginAuthority = "coap://origin.example"

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
		UnsafeVolatileSenderSequence: true,
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
	if authority != testOriginAuthority {
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
		UnsafeVolatileSenderSequence: true,
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
	client, server := pairedContexts(t, 0, 0)
	protected, _, err := client.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://origin.example/a%2Fb?q=one+two"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, last, err := protected.Options.Find(message.ProxyURI)
	if err != nil {
		t.Fatal(err)
	}
	if last-first != 1 || string(protected.Options[first].Value) != testOriginAuthority {
		t.Fatalf("outer Proxy-Uri = %#v", protected.Options[first:last])
	}
	if !protected.Options.HasOption(message.OSCORE) || len(protected.Payload) == 0 {
		t.Fatalf("protected message missing OSCORE data: %+v", protected)
	}

	opened, _, err := server.UnprotectRequest(protected)
	if err != nil {
		t.Fatal(err)
	}
	first, last, err = opened.Options.Find(message.ProxyURI)
	if err != nil {
		t.Fatal(err)
	}
	if last-first != 1 || string(opened.Options[first].Value) != testOriginAuthority {
		t.Fatalf("decrypted Proxy-Uri options = %#v", opened.Options[first:last])
	}
	paths := make([]string, len(opened.Options))
	pathCount, err := opened.Options.GetStrings(message.URIPath, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths[:pathCount]; len(got) != 1 || got[0] != "a/b" {
		t.Fatalf("decrypted Uri-Path segments = %#v", got)
	}
	queries := make([]string, len(opened.Options))
	queryCount, err := opened.Options.GetStrings(message.URIQuery, queries)
	if err != nil {
		t.Fatal(err)
	}
	if got := queries[:queryCount]; len(got) != 1 || got[0] != "q=one+two" {
		t.Fatalf("decrypted Uri-Query values = %#v", got)
	}
}
