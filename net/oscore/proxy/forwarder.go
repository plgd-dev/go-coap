// Package proxy provides a minimal opaque forwarder for OSCORE-protected
// CoAP requests. It routes on the outer Proxy-Uri authority and does not
// inspect or modify the encrypted request payload or OSCORE option.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

var (
	ErrInvalidRequest         = errors.New("invalid OSCORE proxy request")
	ErrBadGateway             = errors.New("invalid OSCORE proxy origin response")
	ErrTargetNotAllowed       = errors.New("OSCORE proxy target is not allowed")
	ErrUnsupportedOperation   = errors.New("unsupported OSCORE proxy operation")
	ErrInvalidForwarderConfig = errors.New("invalid OSCORE proxy forwarder configuration")
)

// RoundTripper sends a CoAP request to the selected target and returns its
// response. Implementations should use a fresh transport connection per
// request unless they provide collision-safe token rewriting.
type RoundTripper interface {
	RoundTrip(context.Context, oscore.ProxyTarget, message.Message) (message.Message, error)
}

// RoundTripperFunc adapts a function to the RoundTripper interface.
type RoundTripperFunc func(context.Context, oscore.ProxyTarget, message.Message) (message.Message, error)

// RoundTrip sends the request using the function.
func (f RoundTripperFunc) RoundTrip(ctx context.Context, target oscore.ProxyTarget, request message.Message) (message.Message, error) {
	return f(ctx, target, request)
}

// TargetPolicy authorizes a destination before the forwarder contacts it.
// Applications should implement an allowlist; a nil policy is not permitted.
// A policy that permits hostnames must ensure they are resolved and the
// resulting IP is pinned by the RoundTripper. Resolving a hostname once for
// policy evaluation but again during dialing permits DNS rebinding and SSRF.
type TargetPolicy func(oscore.ProxyTarget) bool

// AllowPublicIPLiteralTarget accepts only literal public IP destinations.
// It rejects hostnames as well as loopback, private, link-local, multicast,
// and unspecified addresses. Applications with DNS targets should instead
// resolve and pin addresses in their policy and RoundTripper.
func AllowPublicIPLiteralTarget(target oscore.ProxyTarget) bool {
	ip, err := netip.ParseAddr(target.Host)
	if err != nil {
		return false
	}
	if ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range deniedPublicTargetPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var deniedPublicTargetPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("3fff::/20"),
}

// Forwarder implements a one-request/one-response OSCORE forwarding path.
// The surrounding CoAP transport handles Outer Blockwise when enabled.
// RoundTripper implementations should use Blockwise for large envelopes.
// The forwarder does not provide caching, Observe, or proxy session management.
// If OnError is nil, forwarding errors are silently ignored after the error
// response is written.
type Forwarder struct {
	RoundTripper RoundTripper
	AllowTarget  TargetPolicy
	OnError      func(error)
}

// Forward validates and routes an opaque OSCORE request. The Proxy-Uri must
// contain only its authority, as produced by Context.ProtectRequest.
func (f *Forwarder) Forward(ctx context.Context, request message.Message) (message.Message, error) {
	target, err := f.validate(request)
	if err != nil {
		return message.Message{}, err
	}
	forwarded, err := prepareForwardedRequest(request, target)
	if err != nil {
		return message.Message{}, err
	}
	response, err := f.RoundTripper.RoundTrip(ctx, target, forwarded)
	if err != nil {
		return message.Message{}, fmt.Errorf("forward OSCORE request to %s: %w", target.AuthorityURI(), err)
	}
	if !oscore.IsResponseCode(response.Code) || !bytes.Equal(response.Token, request.Token) {
		return message.Message{}, fmt.Errorf("%w: origin returned an invalid response code or token", ErrBadGateway)
	}
	return response, nil
}

func (f *Forwarder) validate(request message.Message) (oscore.ProxyTarget, error) {
	if f == nil || f.RoundTripper == nil || f.AllowTarget == nil {
		return oscore.ProxyTarget{}, ErrInvalidForwarderConfig
	}
	if request.Code != codes.POST {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: outer request code must be POST", ErrUnsupportedOperation)
	}
	proxyURI, foundProxyURI := singleOption(request.Options, message.ProxyURI)
	if !foundProxyURI {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: missing Proxy-Uri", ErrInvalidRequest)
	}
	oscoreOption, foundOSCORE := singleOption(request.Options, message.OSCORE)
	if !foundOSCORE {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: missing OSCORE option", ErrInvalidRequest)
	}
	if _, err := oscore.UnmarshalOption(oscoreOption.Value); err != nil {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: malformed OSCORE option: %w", ErrInvalidRequest, err)
	}
	for _, option := range request.Options {
		if oscore.ClassifyOption(option.ID) == oscore.ClassE {
			return oscore.ProxyTarget{}, fmt.Errorf("%w: exposed Class E option %d", ErrInvalidRequest, option.ID)
		}
	}
	target, err := oscore.ParseProxyURI(string(proxyURI.Value))
	if err != nil {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: invalid Proxy-Uri: %w", ErrInvalidRequest, err)
	}
	if len(target.Path) != 0 || len(target.Query) != 0 {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: Proxy-Uri must contain only an authority", ErrInvalidRequest)
	}
	if !f.AllowTarget(target) {
		return oscore.ProxyTarget{}, fmt.Errorf("%w: %s", ErrTargetNotAllowed, target.AuthorityURI())
	}
	return target, nil
}

func prepareForwardedRequest(request message.Message, target oscore.ProxyTarget) (message.Message, error) {
	forwarded := cloneMessage(request)
	forwarded.Options = forwarded.Options[:0]
	for _, option := range request.Options {
		switch option.ID {
		case message.ProxyURI, message.ProxyScheme, message.URIHost, message.URIPort:
			continue
		default:
			forwarded.Options = append(forwarded.Options, message.Option{
				ID: option.ID, Value: append([]byte(nil), option.Value...),
			})
		}
	}
	forwarded.Options = append(forwarded.Options, message.Option{
		ID: message.URIHost, Value: []byte(target.Host),
	})
	if target.Port != defaultPort(target.Scheme) {
		var encodedPort [4]byte
		port, parseErr := strconv.ParseUint(target.Port, 10, 32)
		if parseErr != nil {
			return message.Message{}, fmt.Errorf("%w: parse destination port: %w", ErrInvalidRequest, parseErr)
		}
		n, encodeErr := message.EncodeUint32(encodedPort[:], uint32(port))
		if encodeErr != nil {
			return message.Message{}, fmt.Errorf("%w: encode destination port: %w", ErrInvalidRequest, encodeErr)
		}
		forwarded.Options = append(forwarded.Options, message.Option{
			ID: message.URIPort, Value: append([]byte(nil), encodedPort[:n]...),
		})
	}
	sortOptions(forwarded.Options)
	return forwarded, nil
}

// ServeCOAP adapts the forwarder to a mux.Handler.
func (f *Forwarder) ServeCOAP(w mux.ResponseWriter, r *mux.Message) {
	body, err := r.ReadBody()
	if err == nil {
		var response message.Message
		response, err = f.Forward(r.Context(), message.Message{
			Token:     r.Token(),
			Code:      r.Code(),
			Type:      r.Type(),
			MessageID: r.MessageID(),
			Options:   oscore.CloneOptions(r.Options()),
			Payload:   body,
		})
		if err == nil {
			out := w.Message()
			out.SetCode(response.Code)
			out.ResetOptionsTo(response.Options)
			out.SetBody(bytes.NewReader(response.Payload))
			return
		}
	}
	f.report(err)
	code := errorCode(err)
	if responseErr := w.SetResponse(code, message.TextPlain, bytes.NewReader([]byte(code.String()))); responseErr != nil {
		f.report(fmt.Errorf("write OSCORE proxy error response: %w", responseErr))
	}
}

func (f *Forwarder) report(err error) {
	if f != nil && f.OnError != nil {
		f.OnError(err)
	}
}

func errorCode(err error) codes.Code {
	switch {
	case errors.Is(err, ErrBadGateway):
		return codes.BadGateway
	case errors.Is(err, ErrInvalidRequest):
		return codes.BadRequest
	case errors.Is(err, ErrTargetNotAllowed):
		return codes.Forbidden
	case errors.Is(err, ErrUnsupportedOperation):
		return codes.ProxyingNotSupported
	default:
		return codes.BadGateway
	}
}

func singleOption(options message.Options, id message.OptionID) (message.Option, bool) {
	var found message.Option
	count := 0
	for _, option := range options {
		if option.ID == id {
			found = option
			count++
		}
	}
	return found, count == 1
}

func cloneMessage(m message.Message) message.Message {
	return message.Message{
		Token:     append(message.Token(nil), m.Token...),
		Code:      m.Code,
		Type:      m.Type,
		MessageID: m.MessageID,
		Options:   oscore.CloneOptions(m.Options),
		Payload:   append([]byte(nil), m.Payload...),
	}
}

func sortOptions(options message.Options) {
	for i := 1; i < len(options); i++ {
		for j := i; j > 0 && options[j].ID < options[j-1].ID; j-- {
			options[j], options[j-1] = options[j-1], options[j]
		}
	}
}

func defaultPort(scheme string) string {
	if scheme == "coaps" {
		return "5684"
	}
	return "5683"
}

var _ mux.Handler = (*Forwarder)(nil)
