package net

import piondtls "github.com/pion/dtls/v3"

// DTLSServerOptions holds DTLS server-side configuration options for use with
// NewDTLSListener. It wraps the options-based API of pion/dtls,
// keeping pion/dtls option types out of go-coap function signatures.
type DTLSServerOptions struct {
	opts []piondtls.ServerOption
}

// NewDTLSServerOptions creates a DTLSServerOptions from the provided pion/dtls
// ServerOption values (e.g. piondtls.WithPSK, piondtls.WithCertificates, …).
//
// Most pion/dtls options implement the shared piondtls.Option interface, which
// satisfies both ServerOption and ClientOption, so they can be passed here
// directly. Server-only options such as piondtls.WithClientAuth are also
// accepted.
func NewDTLSServerOptions(opts ...piondtls.ServerOption) DTLSServerOptions {
	return DTLSServerOptions{opts: opts}
}

// DTLSServerConfig is a type constraint accepted by NewDTLSListener,
// ListenAndServeDTLS and ListenAndServeDTLSWithOptions.
// It allows callers to pass either the legacy *piondtls.Config
// (backward-compatible) or the recommended DTLSServerOptions wrapper
// (built via NewDTLSServerOptions).
type DTLSServerConfig interface {
	*piondtls.Config | DTLSServerOptions
}
