package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

const (
	testOriginHost = "origin.example"
	testPublicIPv4 = "8.8.8.8"
)

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
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	originContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
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
		AllowTarget: func(_ context.Context, target oscore.ProxyTarget) bool {
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
	forwarder := Forwarder{RoundTripper: RoundTripperFunc(func(
		context.Context, oscore.ProxyTarget, message.Message,
	) (message.Message, error) {
		return message.Message{}, nil
	})}
	_, err := forwarder.Forward(context.Background(), validProtectedProxyRequest(t))
	if !errors.Is(err, ErrInvalidForwarderConfig) {
		t.Fatalf("Forward error = %v", err)
	}
}

func TestForwarderRejectsDisallowedTargetBeforeDial(t *testing.T) {
	secret := []byte("0123456789abcdef")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
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
		AllowTarget:  func(context.Context, oscore.ProxyTarget) bool { return false },
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
				AllowTarget: func(context.Context, oscore.ProxyTarget) bool { return true },
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
		port string
		want bool
	}{
		{host: testPublicIPv4, port: "5683", want: true},
		{host: testPublicIPv4, port: "5684", want: true},
		{host: "2001:4860:4860::8888", port: "5683", want: true},
		{host: "::ffff:" + testPublicIPv4, port: "5683", want: true},
		{host: testPublicIPv4, port: "8080"},
		{host: "127.0.0.1"},
		{host: "10.0.0.1"},
		{host: "169.254.1.1"},
		{host: "::1"},
		{host: "fe80::1"},
		{host: "100.64.0.1"},
		{host: "64:ff9b::a00:1%eth0"},
		{host: "fec0::1"},
		{host: "100::1"},
		{host: "5f00::1"},
		{host: "5f00:1234::1"},
		{host: "::1.2.3.4"},
		{host: "0.1.2.3"},
		{host: "192.0.0.1"},
		{host: "192.0.2.1"},
		{host: "192.88.99.1"},
		{host: "198.18.0.1"},
		{host: "198.51.100.1"},
		{host: "203.0.113.1"},
		{host: "240.0.0.1"},
		{host: "255.255.255.255"},
		{host: "64:ff9b::808:808"},
		{host: "64:ff9b:1::a00:1"},
		{host: "2001::1"},
		{host: "2001:2::1"},
		{host: "2001:db8::1"},
		{host: "2002::1"},
		{host: "3fff::1"},
		{host: "::ffff:192.0.2.1"},
		{host: "::ffff:100.64.0.1"},
		{host: "origin.example"},
	} {
		t.Run(test.host, func(t *testing.T) {
			port := test.port
			if port == "" {
				port = "5683"
			}
			if got := AllowPublicIPLiteralTarget(context.Background(), oscore.ProxyTarget{
				Host: test.host, Port: port,
			}); got != test.want {
				t.Fatalf("AllowPublicIPLiteralTarget(%q:%q) = %t, want %t",
					test.host, port, got, test.want)
			}
		})
	}
}

func TestAllowPublicIPLiteralTargetConfigurablePorts(t *testing.T) {
	target := oscore.ProxyTarget{Host: testPublicIPv4, Port: "8443"}
	if !AllowPublicIPLiteralTargetWithPorts(context.Background(), target, 8443) {
		t.Fatal("configured public target port was rejected")
	}
	if AllowPublicIPLiteralTargetWithPorts(context.Background(), target, 5683, 5684) {
		t.Fatal("unconfigured public target port was allowed")
	}
}

func TestForwarderRateLimitRejectsBeforeRoundTrip(t *testing.T) {
	originCalled := false
	peer := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 5683}
	var rateLimitedPeer net.Addr
	forwarder := Forwarder{
		RoundTripper: RoundTripperFunc(func(context.Context, oscore.ProxyTarget, message.Message) (message.Message, error) {
			originCalled = true
			return message.Message{}, nil
		}),
		AllowTarget: func(_ context.Context, target oscore.ProxyTarget) bool {
			return target.Host == testOriginHost
		},
		RateLimit: func(_ context.Context, gotPeer net.Addr, target oscore.ProxyTarget) bool {
			rateLimitedPeer = gotPeer
			return target.Host != testOriginHost
		},
	}
	_, err := forwarder.ForwardFrom(context.Background(), peer, validProtectedProxyRequest(t))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Forward error = %v, want ErrRateLimited", err)
	}
	if got := errorCode(err); got != codes.TooManyRequests {
		t.Fatalf("errorCode = %v, want TooManyRequests", got)
	}
	if originCalled {
		t.Fatal("round trip attempted for a rate-limited request")
	}
	if rateLimitedPeer == nil || rateLimitedPeer.String() != peer.String() {
		t.Fatalf("rate limiter peer = %v, want %v", rateLimitedPeer, peer)
	}
}

func TestForwarderReportUsesOnErrorCallback(t *testing.T) {
	want := errors.New("forwarding failed")
	var got error
	(&Forwarder{OnError: func(err error) {
		got = err
	}}).report(want)
	if !errors.Is(got, want) {
		t.Fatalf("OnError received %v, want %v", got, want)
	}

	// Without a callback, reporting is intentionally a no-op.
	(&Forwarder{}).report(want)
}

func validProtectedProxyRequest(t *testing.T) message.Message {
	t.Helper()
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
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

func TestForwarderRelaysEDHOCCombinedRequest(t *testing.T) {
	secret := []byte("0123456789abcdef")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	originContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, exchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET,
		Options: message.Options{{
			ID:    message.ProxyURI,
			Value: []byte("coap://" + testOriginHost + ":5683"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	message3 := []byte{0x43, 0x01, 0x02, 0x03}
	request.Options = request.Options.Add(message.Option{ID: message.EDHOC})
	request.Payload = append(append([]byte(nil), message3...), request.Payload...)
	combinedPayload := append([]byte(nil), request.Payload...)
	forwarder := Forwarder{
		RoundTripper: RoundTripperFunc(func(_ context.Context, _ oscore.ProxyTarget, forwarded message.Message) (message.Message, error) {
			if !forwarded.Options.HasOption(message.EDHOC) {
				return message.Message{}, errors.New("forwarder removed the EDHOC option")
			}
			if !bytes.Equal(forwarded.Payload, combinedPayload) {
				return message.Message{}, errors.New("forwarder changed the EDHOC + OSCORE payload")
			}
			forwarded.Options = oscore.CloneOptions(forwarded.Options).Remove(message.EDHOC)
			forwarded.Payload = forwarded.Payload[len(message3):]
			_, originExchange, unprotectErr := originContext.UnprotectRequest(forwarded)
			if unprotectErr != nil {
				return message.Message{}, unprotectErr
			}
			return originContext.ProtectResponse(message.Message{Code: codes.Content}, originExchange)
		}),
		AllowTarget: func(_ context.Context, target oscore.ProxyTarget) bool {
			return target.Host == testOriginHost
		},
	}
	response, err := forwarder.Forward(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := clientContext.UnprotectResponse(response, exchange)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Code != codes.Content {
		t.Fatalf("opened response = %+v", opened)
	}
}
