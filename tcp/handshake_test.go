package tcp_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTLSServerPendingHandshakes(t *testing.T) {
	serverCfg, clientCfg, _, err := createTLSConfig()
	require.NoError(t, err)
	listener, err := coapNet.NewTLSListener("tcp4", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	errs := make(chan error, 10)
	newConn := make(chan bool, 1)
	server := tcp.NewServer(
		options.WithHandshakeTimeout(time.Hour),
		options.WithMaxPendingHandshakes(2),
		options.WithDisableTCPSignalMessageCSM(),
		options.WithErrors(func(err error) { errs <- err }),
		options.WithOnNewConn(func(cc *client.Conn) {
			conn, ok := cc.NetConn().(*tls.Conn)
			newConn <- ok && conn.ConnectionState().HandshakeComplete && len(conn.ConnectionState().PeerCertificates) > 0
		}),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		server.Stop()
		wg.Wait()
	}()

	addresses := make([]string, 0, 3)
	for range 3 {
		conn, dialErr := net.Dial("tcp4", listener.Addr().String())
		require.NoError(t, dialErr)
		addresses = append(addresses, conn.LocalAddr().String())
		t.Cleanup(func() { _ = conn.Close() })
	}
	select {
	case err = <-errs:
		require.ErrorContains(t, err, "handshake failed: too many pending handshakes")
		require.ErrorContains(t, err, addresses[0])
	case <-time.After(5 * time.Second):
		t.Fatal("oldest TLS handshake was not evicted")
	}
	select {
	case <-newConn:
		t.Fatal("OnNewConn called before TLS handshake")
	default:
	}

	conn, err := tls.Dial("tcp4", listener.Addr().String(), clientCfg)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	select {
	case err = <-errs:
		require.ErrorContains(t, err, "handshake failed: too many pending handshakes")
		require.ErrorContains(t, err, addresses[1])
	case <-time.After(5 * time.Second):
		t.Fatal("second TLS handshake was not evicted")
	}
	select {
	case verified := <-newConn:
		require.True(t, verified)
	case <-time.After(5 * time.Second):
		t.Fatal("completed TLS handshake did not reach OnNewConn")
	}
}

func TestTLSServerHandshakeTimeout(t *testing.T) {
	serverCfg, _, _, err := createTLSConfig()
	require.NoError(t, err)
	listener, err := coapNet.NewTLSListener("tcp4", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	errs := make(chan error, 1)
	var accepted atomic.Bool
	server := tcp.NewServer(
		options.WithHandshakeTimeout(50*time.Millisecond),
		options.WithErrors(func(err error) { errs <- err }),
		options.WithOnNewConn(func(*client.Conn) { accepted.Store(true) }),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		server.Stop()
		wg.Wait()
	}()
	conn, err := net.Dial("tcp4", listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	select {
	case err = <-errs:
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorContains(t, err, "handshake failed")
	case <-time.After(5 * time.Second):
		t.Fatal("TLS handshake timeout not reported")
	}
	require.False(t, accepted.Load())
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	_, err = conn.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF)
}

func TestPlainTCPNotLimitedByHandshakeOptions(t *testing.T) {
	listener, err := coapNet.NewTCPListener("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	accepted := make(chan struct{}, 2)
	server := tcp.NewServer(
		options.WithHandshakeTimeout(time.Millisecond),
		options.WithMaxPendingHandshakes(1),
		options.WithDisableTCPSignalMessageCSM(),
		options.WithOnNewConn(func(*client.Conn) { accepted <- struct{}{} }),
		options.WithErrors(func(err error) {
			if strings.Contains(err.Error(), "handshake failed") {
				assert.Fail(t, "plain TCP handshake error", "%v", err)
			}
		}),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		server.Stop()
		wg.Wait()
	}()
	for range 2 {
		conn, dialErr := net.Dial("tcp4", listener.Addr().String())
		require.NoError(t, dialErr)
		t.Cleanup(func() { _ = conn.Close() })
	}
	for range 2 {
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatal("plain TCP connection was not accepted")
		}
	}
}

type pendingTCPListener struct {
	conns chan net.Conn
}

func (l *pendingTCPListener) AcceptWithContext(ctx context.Context) (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *pendingTCPListener) Close() error { return nil }

type pendingTCPConn struct {
	net.Conn
	started  chan<- struct{}
	canceled chan<- struct{}
}

func (c *pendingTCPConn) HandshakeContext(ctx context.Context) error {
	c.started <- struct{}{}
	<-ctx.Done()
	c.canceled <- struct{}{}
	return ctx.Err()
}

func TestTCPServerUnlimitedHandshakesAndStop(t *testing.T) {
	const count = 8
	listener := &pendingTCPListener{conns: make(chan net.Conn, count)}
	started := make(chan struct{}, count)
	canceled := make(chan struct{}, count)
	server := tcp.NewServer(options.WithHandshakeTimeout(0), options.WithMaxPendingHandshakes(0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		server.Stop()
		<-done
	}()
	for range count {
		peer, conn := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		listener.conns <- &pendingTCPConn{Conn: conn, started: started, canceled: canceled}
	}
	for range count {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("handshake did not start")
		}
	}
	select {
	case <-canceled:
		t.Fatal("handshake canceled with limit and timeout disabled")
	default:
	}
	server.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop pending handshakes")
	}
	require.Len(t, canceled, count)
}
