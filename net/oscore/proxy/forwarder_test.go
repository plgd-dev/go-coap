package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

const testOriginHost = "origin.example"

type testOrigin struct {
	context         *oscore.Context
	expectedPayload []byte
	called          bool
}

func (o *testOrigin) RoundTrip(_ context.Context, target oscore.ProxyTarget, request message.Message) (message.Message, error) {
	o.called = true
	if target.Host != testOriginHost || target.Port != "5683" || target.Scheme != "coap" {
		return message.Message{}, errors.New("unexpected target")
	}
	if !bytes.Equal(request.Payload, o.expectedPayload) {
		return message.Message{}, errors.New("forwarder changed OSCORE ciphertext")
	}
	if request.Options.HasOption(message.ProxyURI) {
		return message.Message{}, errors.New("forwarder left Proxy-Uri on the origin request")
	}
	if host, err := request.Options.GetString(message.URIHost); err != nil || host != testOriginHost {
		return message.Message{}, errors.New("forwarder did not set origin Uri-Host")
	}
	opened, exchange, err := o.context.UnprotectRequest(request)
	if err != nil {
		return message.Message{}, err
	}
	path, err := opened.Options.Path()
	if err != nil {
		return message.Message{}, fmt.Errorf("read origin Uri-Path: %w", err)
	}
	if path != "/sensors/temp" {
		return message.Message{}, fmt.Errorf("origin received incorrect protected Uri-Path %q", path)
	}
	queries, err := opened.Options.Queries()
	if err != nil || len(queries) != 1 || queries[0] != "unit=celsius" {
		return message.Message{}, errors.New("origin received incorrect protected Uri-Query")
	}
	return o.context.ProtectResponse(message.Message{
		Code:    codes.Content,
		Payload: []byte("temperature"),
	}, exchange)
}

func TestForwarderRelaysOSCOREWithoutDecrypting(t *testing.T) {
	secret := []byte("0123456789abcdef")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          secret,
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	originContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          secret,
		SenderID:              []byte{1},
		RecipientID:           []byte{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, exchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://" + testOriginHost + ":5683/sensors/temp?unit=celsius"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := &testOrigin{context: originContext, expectedPayload: append([]byte(nil), request.Payload...)}
	forwarder := Forwarder{
		RoundTripper: origin,
		AllowTarget: func(target oscore.ProxyTarget) bool {
			return target.Host == testOriginHost
		},
	}

	response, err := forwarder.Forward(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !origin.called {
		t.Fatal("origin was not called")
	}
	opened, err := clientContext.UnprotectResponse(response, exchange)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != codes.Content || string(opened.Payload) != "temperature" {
		t.Fatalf("opened response = %+v", opened)
	}
}

func TestForwarderRequiresTargetAllowlist(t *testing.T) {
	forwarder := Forwarder{}
	_, err := forwarder.Forward(context.Background(), message.Message{})
	if !errors.Is(err, ErrInvalidForwarderConfig) {
		t.Fatalf("Forward error = %v", err)
	}
}

func TestForwarderRejectsDisallowedTargetBeforeDial(t *testing.T) {
	secret := []byte("0123456789abcdef")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          secret,
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://blocked.example/resource"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	origin := &testOrigin{}
	forwarder := Forwarder{
		RoundTripper: origin,
		AllowTarget:  func(oscore.ProxyTarget) bool { return false },
	}
	_, err = forwarder.Forward(context.Background(), request)
	if !errors.Is(err, ErrTargetNotAllowed) {
		t.Fatalf("Forward error = %v", err)
	}
	if origin.called {
		t.Fatal("round trip attempted for a denied target")
	}
}

func TestForwarderMapsInvalidOriginResponseToBadGateway(t *testing.T) {
	tests := []struct {
		name     string
		response message.Message
	}{
		{name: "invalid code", response: message.Message{Code: codes.Empty}},
		{name: "invalid token", response: message.Message{Code: codes.Content, Token: []byte{2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forwarder := Forwarder{
				RoundTripper: RoundTripperFunc(func(context.Context, oscore.ProxyTarget, message.Message) (message.Message, error) {
					return test.response, nil
				}),
				AllowTarget: func(oscore.ProxyTarget) bool { return true },
			}
			_, err := forwarder.Forward(context.Background(), validProtectedProxyRequest(t))
			if !errors.Is(err, ErrBadGateway) {
				t.Fatalf("Forward error = %v, want ErrBadGateway", err)
			}
			if got := errorCode(err); got != codes.BadGateway {
				t.Fatalf("errorCode = %v, want 5.02", got)
			}
		})
	}
}

func TestAllowPublicIPLiteralTarget(t *testing.T) {
	for _, test := range []struct {
		host string
		want bool
	}{
		{host: "8.8.8.8", want: true},
		{host: "2001:4860:4860::8888", want: true},
		{host: "::ffff:8.8.8.8", want: true},
		{host: "127.0.0.1"},
		{host: "10.0.0.1"},
		{host: "169.254.1.1"},
		{host: "::1"},
		{host: "fe80::1"},
		{host: "100.64.0.1"},
		{host: "64:ff9b::a00:1%eth0"},
		{host: "0.1.2.3"},
		{host: "192.0.0.1"},
		{host: "192.0.2.1"},
		{host: "198.18.0.1"},
		{host: "198.51.100.1"},
		{host: "203.0.113.1"},
		{host: "240.0.0.1"},
		{host: "255.255.255.255"},
		{host: "64:ff9b::808:808"},
		{host: "64:ff9b:1::a00:1"},
		{host: "2001:2::1"},
		{host: "2001:db8::1"},
		{host: "3fff::1"},
		{host: "::ffff:192.0.2.1"},
		{host: "::ffff:100.64.0.1"},
		{host: "origin.example"},
	} {
		t.Run(test.host, func(t *testing.T) {
			if got := AllowPublicIPLiteralTarget(oscore.ProxyTarget{Host: test.host}); got != test.want {
				t.Fatalf("AllowPublicIPLiteralTarget(%q) = %t, want %t", test.host, got, test.want)
			}
		})
	}
}

func validProtectedProxyRequest(t *testing.T) message.Message {
	t.Helper()
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("0123456789abcdef"),
		SenderID:              []byte{0},
		RecipientID:           []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://" + testOriginHost + "/sensors/temp"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}
