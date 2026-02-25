package dtls

import piondtls "github.com/pion/dtls/v3"

// DTLSClientOptions holds DTLS client-side configuration options for use with
// DialWithOptions. It wraps the options-based API of pion/dtls, keeping
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
