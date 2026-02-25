package dtls

import piondtls "github.com/pion/dtls/v3"

// DTLSClientOptions holds DTLS client-side configuration options for use with
// Dial. It wraps the options-based API of pion/dtls, keeping
// pion/dtls option types out of go-coap function signatures.
type DTLSClientOptions struct {
	opts []piondtls.ClientOption
}

// NewDTLSClientOptions creates a DTLSClientOptions from the provided pion/dtls
// ClientOption values (e.g. piondtls.WithPSK, piondtls.WithCertificates, …).
//
// Most pion/dtls options implement the shared piondtls.Option interface, which
// satisfies both ServerOption and ClientOption, so they can be passed here
// directly.
func NewDTLSClientOptions(opts ...piondtls.ClientOption) DTLSClientOptions {
	return DTLSClientOptions{opts: opts}
}

// DTLSClientConfig is a type constraint accepted by Dial.
// It allows callers to pass either the legacy *piondtls.Config
// (backward-compatible) or the recommended DTLSClientOptions wrapper
// (built via NewDTLSClientOptions).
type DTLSClientConfig interface {
	*piondtls.Config | DTLSClientOptions
}
