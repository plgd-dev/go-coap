package net

import (
	"context"
	"fmt"
	"net"

	dtls "github.com/pion/dtls/v3"
	"go.uber.org/atomic"
)

// DTLSListener is a DTLS listener that provides accept with context.
type DTLSListener struct {
	listener net.Listener
	config   *dtls.Config
	closed   atomic.Bool
}

// NewDTLSListener creates dtls listener.
// Known networks are "udp", "udp4" (IPv4-only), "udp6" (IPv6-only).
func NewDTLSListener(network string, addr string, dtlsCfg *dtls.Config) (*DTLSListener, error) {
	a, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve address: %w", err)
	}
	dtls, err := dtls.Listen(network, a, dtlsCfg)
	if err != nil {
		return nil, fmt.Errorf("cannot create new net tcp listener: %w", err)
	}
	return &DTLSListener{
		listener: dtls,
		config:   dtlsCfg,
	}, nil
}

// AcceptWithContext waits with context for a generic Conn.
func (l *DTLSListener) AcceptWithContext(ctx context.Context) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if l.closed.Load() {
		return nil, ErrListenerIsClosed
	}
	rw, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	return rw, nil
}

// Accept waits for a generic Conn.
func (l *DTLSListener) Accept() (net.Conn, error) {
	return l.AcceptWithContext(context.Background())
}

// Close closes the connection.
func (l *DTLSListener) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	return l.listener.Close()
}

// Addr represents a network end point address.
func (l *DTLSListener) Addr() net.Addr {
	return l.listener.Addr()
}
