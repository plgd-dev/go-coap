package coap

import (
	"context"
	"crypto/x509"
	"net"
)

// A networkSession interface is used by an COAP handler to
// server data in session.
type networkSession interface {
	// LocalAddr returns the net.Addr of the server
	LocalAddr() net.Addr
	// RemoteAddr returns the net.Addr of the client that sent the current request.
	RemoteAddr() net.Addr
	// PeerCertificates returns the certificate chain presented by the remote
	// peer of the current request.
	PeerCertificates() []*x509.Certificate
	// WriteContextMsg writes a reply back to the client.
	WriteMsgWithContext(ctx context.Context, resp Message) error
	// Close closes the connection.
	Close() error
	// Return type of network
	IsTCP() bool
	// Create message for response via writter
	NewMessage(params MessageParams) Message
	// ExchangeContext writes message and wait for response - paired by token and msgid
	// it is safe to use in goroutines
	ExchangeWithContext(ctx context.Context, req Message) (Message, error)
	// Send ping to peer and wait for pong
	PingWithContext(ctx context.Context) error
	// Sequence discontinuously unique growing number for connection.
	Sequence() uint64
	// Session notifies via close all goroutines depends on session
	Done() <-chan struct{}

	// handlePairMsg Message was handled by pair
	handlePairMsg(w ResponseWriter, r *Request) bool

	// handleSignals Message below to signals
	handleSignals(w ResponseWriter, r *Request) bool

	// sendPong create pong by m and send it
	sendPong(w ResponseWriter, r *Request) error

	// close session with error
	closeWithError(err error) error

	TokenHandler() *TokenHandler

	// BlockWiseTransferEnabled
	blockWiseEnabled() bool
	// BlockWiseTransferSzx
	blockWiseSzx() BlockWiseSzx
	// MaxPayloadSize
	blockWiseMaxPayloadSize(peer BlockWiseSzx) (int, BlockWiseSzx)

	blockWiseIsValid(szx BlockWiseSzx) bool
}

func handleSignalMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.Client.networkSession().handleSignals(w, r) {
		next(w, r)
	}
}

func handlePairMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.Client.networkSession().handlePairMsg(w, r) {
		next(w, r)
	}
}
