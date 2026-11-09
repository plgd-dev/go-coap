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
		MasterSecret: secret,
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	originContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: secret,
		SenderID:     []byte{1},
		RecipientID:  []byte{0},
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
		MasterSecret: secret,
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
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
