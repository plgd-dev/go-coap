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
	closed   atomic.Bool
}

// newNetDTLSListener is the internal helper for the legacy *dtls.Config-based path.
//
// Deprecated: use newNetDTLSListenerWithOptions instead.
func newNetDTLSListener(network string, addr string, dtlsCfg *dtls.Config) (net.Listener, error) {
	a, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve address: %w", err)
	}
	dtls, err := dtls.Listen(network, a, dtlsCfg)
	if err != nil {
		return nil, fmt.Errorf("cannot create new net dtls listener: %w", err)
	}
	return dtls, nil
}

func newNetDTLSListenerWithOptions(network string, addr string, dtlsOpts DTLSServerOptions) (net.Listener, error) {
	a, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve address: %w", err)
	}
	l, err := dtls.ListenWithOptions(network, a, dtlsOpts.opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create new net dtls listener: %w", err)
	}
	return l, nil
}

// NewDTLSListener creates a DTLS listener.
// Known networks are "udp", "udp4" (IPv4-only), "udp6" (IPv6-only).
// cfg accepts either a *dtls.Config (backward-compatible legacy path) or a
// DTLSServerOptions value built with NewDTLSServerOptions (recommended).
func NewDTLSListener[T DTLSServerConfig](network, addr string, cfg T) (*DTLSListener, error) {
	switch v := any(cfg).(type) {
	case *dtls.Config:
		l, err := newNetDTLSListener(network, addr, v)
		if err != nil {
			return nil, fmt.Errorf("cannot create new dtls listener: %w", err)
		}
		return &DTLSListener{listener: l}, nil
	case DTLSServerOptions:
		l, err := newNetDTLSListenerWithOptions(network, addr, v)
		if err != nil {
			return nil, fmt.Errorf("cannot create new dtls listener: %w", err)
		}
		return &DTLSListener{listener: l}, nil
	default:
		panic("unreachable: unexpected type in DTLSServerConfig constraint")
	}
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
	return l.listener.Accept()
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
