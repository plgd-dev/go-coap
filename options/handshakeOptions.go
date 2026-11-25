package options

import (
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	tcpServer "github.com/plgd-dev/go-coap/v3/tcp/server"
)

// HandshakeTimeoutOpt configures the server-side TLS or DTLS handshake timeout.
type HandshakeTimeoutOpt struct {
	timeout time.Duration
}

func (o HandshakeTimeoutOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.HandshakeTimeout = o.timeout
}

func (o HandshakeTimeoutOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.HandshakeTimeout = o.timeout
}

// WithHandshakeTimeout sets the TLS/DTLS server handshake timeout before OnNewConn.
// The default is 30 seconds; nonpositive values disable the timeout. Handshake
// failures other than cancellation or closed-connection errors are reported to
// the error callback with "handshake failed".
func WithHandshakeTimeout(timeout time.Duration) HandshakeTimeoutOpt {
	return HandshakeTimeoutOpt{timeout: timeout}
}

// DTLSHandshakeTimeoutOpt is an alias for HandshakeTimeoutOpt.
type DTLSHandshakeTimeoutOpt = HandshakeTimeoutOpt

// WithDTLSHandshakeTimeout is an alias for WithHandshakeTimeout.
func WithDTLSHandshakeTimeout(timeout time.Duration) DTLSHandshakeTimeoutOpt {
	return WithHandshakeTimeout(timeout)
}

// MaxPendingHandshakesOpt configures the server-side TLS/DTLS handshake limit.
type MaxPendingHandshakesOpt struct {
	limit uint32
}

func (o MaxPendingHandshakesOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.MaxPendingHandshakes = o.limit
}

func (o MaxPendingHandshakesOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.MaxPendingHandshakes = o.limit
}

// WithMaxPendingHandshakes limits concurrent server-side TLS/DTLS handshakes
// (default 1024). At the limit, the oldest handshake is cancelled to admit a
// new one and reported as "handshake failed" through the error callback. A
// sustained flood can still prevent slow handshakes from completing. Large
// fleets may need a higher limit for simultaneous reconnects. Zero disables
// the limit and allows unbounded resource consumption. Plain TCP connections
// are unaffected.
func WithMaxPendingHandshakes(limit uint32) MaxPendingHandshakesOpt {
	return MaxPendingHandshakesOpt{limit: limit}
}

// DTLSMaxPendingHandshakesOpt is an alias for MaxPendingHandshakesOpt.
type DTLSMaxPendingHandshakesOpt = MaxPendingHandshakesOpt

// WithDTLSMaxPendingHandshakes is an alias for WithMaxPendingHandshakes.
func WithDTLSMaxPendingHandshakes(limit uint32) DTLSMaxPendingHandshakesOpt {
	return WithMaxPendingHandshakes(limit)
}
