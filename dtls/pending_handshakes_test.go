package dtls_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/internal/handshake"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type controlledHandshakeConn struct {
	net.Conn
	id       int
	started  chan<- int
	canceled chan<- int
	release  <-chan struct{}
}

func pendingHandshakeErrorHandler(t *testing.T, abandoned *atomic.Int32, stopping *atomic.Bool) func(error) {
	t.Helper()
	return func(err error) {
		switch {
		case errors.Is(err, handshake.ErrTooManyPending):
			abandoned.Add(1)
		case stopping.Load() && (errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed)):
			return
		default:
			assert.Fail(t, "unexpected server error", "%v", err)
		}
	}
}

func (c *controlledHandshakeConn) HandshakeContext(ctx context.Context) error {
	c.started <- c.id
	<-ctx.Done()
	c.canceled <- c.id
	if c.release != nil {
		<-c.release
	}
	return ctx.Err()
}

func TestServerPendingHandshakeAdmission(t *testing.T) {
	const limit, count = 8, 32
	listener := &singleConnListener{ch: make(chan net.Conn, count)}
	started := make(chan int, count)
	canceled := make(chan int, count)
	var abandoned atomic.Int32
	var stopping atomic.Bool
	server := dtls.NewServer(
		options.WithDTLSMaxPendingHandshakes(limit),
		options.WithDTLSHandshakeTimeout(time.Hour),
		options.WithErrors(pendingHandshakeErrorHandler(t, &abandoned, &stopping)),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		stopping.Store(true)
		server.Stop()
		wg.Wait()
	}()

	for i := range count {
		client, conn := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })
		t.Cleanup(func() { _ = conn.Close() })
		listener.ch <- &controlledHandshakeConn{Conn: conn, id: i, started: started, canceled: canceled}
		select {
		case id := <-started:
			require.Equal(t, i, id)
		case <-time.After(Timeout):
			t.Fatal("handshake did not start")
		}
		if i >= limit {
			select {
			case id := <-canceled:
				require.Equal(t, i-limit, id)
			case <-time.After(Timeout):
				t.Fatal("oldest handshake was not canceled")
			}
		}
	}
	require.Eventually(t, func() bool { return abandoned.Load() == count-limit }, Timeout, time.Millisecond)
}

func TestServerWaitsForEvictedHandshakeCleanup(t *testing.T) {
	listener := &singleConnListener{ch: make(chan net.Conn, 2)}
	started := make(chan int, 2)
	canceled := make(chan int, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := dtls.NewServer(options.WithDTLSMaxPendingHandshakes(1))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		server.Stop()
		wg.Wait()
	}()
	for i := range 2 {
		client, conn := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })
		var wait <-chan struct{}
		if i == 0 {
			wait = release
		}
		listener.ch <- &controlledHandshakeConn{Conn: conn, id: i, started: started, canceled: canceled, release: wait}
		if i == 0 {
			select {
			case id := <-started:
				require.Equal(t, 0, id)
			case <-time.After(Timeout):
				t.Fatal("first handshake did not start")
			}
		}
	}
	select {
	case id := <-canceled:
		require.Equal(t, 0, id)
	case <-time.After(Timeout):
		t.Fatal("oldest handshake was not canceled")
	}
	select {
	case id := <-started:
		t.Fatalf("handshake %d started before eviction completed", id)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case id := <-started:
		require.Equal(t, 1, id)
	case <-time.After(Timeout):
		t.Fatal("replacement handshake did not start")
	}
}

func TestServerUnlimitedPendingHandshakes(t *testing.T) {
	const count = 16
	listener := &singleConnListener{ch: make(chan net.Conn, count)}
	started := make(chan int, count)
	canceled := make(chan int, count)
	server := dtls.NewServer(options.WithDTLSMaxPendingHandshakes(0), options.WithDTLSHandshakeTimeout(time.Hour))
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
	for i := range count {
		client, conn := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })
		listener.ch <- &controlledHandshakeConn{Conn: conn, id: i, started: started, canceled: canceled}
	}
	for range count {
		select {
		case <-started:
		case <-time.After(Timeout):
			t.Fatal("handshake did not start")
		}
	}
	select {
	case id := <-canceled:
		t.Fatalf("handshake %d canceled with limit disabled", id)
	default:
	}
}

func TestServerDefaultPendingHandshakeLimit(t *testing.T) {
	const limit, count = 1024, 1056
	listener := &singleConnListener{ch: make(chan net.Conn, count)}
	started := make(chan int, count)
	canceled := make(chan int, count+limit)
	var abandoned atomic.Int32
	var stopping atomic.Bool
	server := dtls.NewServer(
		options.WithDTLSHandshakeTimeout(time.Hour),
		options.WithErrors(pendingHandshakeErrorHandler(t, &abandoned, &stopping)),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		stopping.Store(true)
		server.Stop()
		wg.Wait()
	}()
	for i := range count {
		client, conn := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })
		t.Cleanup(func() { _ = conn.Close() })
		listener.ch <- &controlledHandshakeConn{Conn: conn, id: i, started: started, canceled: canceled}
	}
	for range count {
		select {
		case <-started:
		case <-time.After(Timeout):
			t.Fatal("handshake did not start")
		}
	}
	require.Eventually(t, func() bool { return abandoned.Load() == count-limit }, Timeout, time.Millisecond)
	require.Len(t, canceled, count-limit)
}

func TestServerPendingHandshakeLimitWithDTLS(t *testing.T) {
	psk := func([]byte) ([]byte, error) { return []byte{0xab, 0xc1, 0x23}, nil }
	serverCfg := coapNet.NewDTLSServerOptions(
		piondtls.WithPSK(psk),
		piondtls.WithPSKIdentityHint([]byte("gateway")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	)
	clientCfg := dtls.NewDTLSClientOptions(
		piondtls.WithPSK(psk),
		piondtls.WithPSKIdentityHint([]byte("gateway")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	)
	listener, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", serverCfg)
	require.NoError(t, err)
	var abandoned atomic.Int32
	var stopping atomic.Bool
	server := dtls.NewServer(
		options.WithDTLSMaxPendingHandshakes(4),
		options.WithDTLSHandshakeTimeout(time.Hour),
		options.WithErrors(pendingHandshakeErrorHandler(t, &abandoned, &stopping)),
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, server.Serve(listener))
	}()
	defer func() {
		stopping.Store(true)
		server.Stop()
		wg.Wait()
	}()

	record := []byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 13, 1, 0, 0, 100, 0, 0, 0, 0, 0, 0, 0, 1, 'A'}
	for i := range 12 {
		conn, dialErr := net.Dial("udp4", listener.Addr().String())
		require.NoError(t, dialErr)
		_, writeErr := conn.Write(record)
		require.NoError(t, writeErr)
		t.Cleanup(func() { _ = conn.Close() })
		if i >= 4 {
			require.Eventually(t, func() bool { return abandoned.Load() >= int32(i-3) }, Timeout, time.Millisecond)
		}
	}
	require.GreaterOrEqual(t, abandoned.Load(), int32(8))
	client, err := dtls.Dial(listener.Addr().String(), clientCfg)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	require.NoError(t, client.Ping(ctx))
}
