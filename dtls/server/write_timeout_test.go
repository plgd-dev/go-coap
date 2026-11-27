package server_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

type blockedPacketConn struct {
	net.PacketConn
	block   atomic.Bool
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.block.Load() {
		c.once.Do(func() { close(c.started) })
		<-c.release
	}
	return c.PacketConn.WriteTo(p, addr)
}

func (c *blockedPacketConn) SetWriteDeadline(time.Time) error { return nil }

type singleConnListener struct {
	conn      net.Conn
	once      sync.Once
	closeOnce sync.Once
	closed    chan struct{}
}

func (l *singleConnListener) AcceptWithContext(ctx context.Context) (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closed:
		return nil, coapNet.ErrListenerIsClosed
	}
}

func (l *singleConnListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func TestServerWriteTimeoutInterruptsBlockedDTLSWrite(t *testing.T) {
	serverSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer serverSocket.Close()
	require.NoError(t, serverSocket.SetWriteBuffer(1024))
	clientSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer clientSocket.Close()
	require.NoError(t, clientSocket.SetReadBuffer(1024))

	psk := piondtls.WithPSK(func([]byte) ([]byte, error) { return []byte{0xab, 0xc1, 0x23}, nil })
	identity := piondtls.WithPSKIdentityHint([]byte("identity"))
	cipher := piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8)
	packet := &blockedPacketConn{
		PacketConn: serverSocket,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	serverConn, err := piondtls.ServerWithOptions(packet, clientSocket.LocalAddr(), psk, identity, cipher)
	require.NoError(t, err)
	clientConn, err := piondtls.ClientWithOptions(clientSocket, serverSocket.LocalAddr(), psk, identity, cipher)
	require.NoError(t, err)
	defer clientConn.Close()
	listener := &singleConnListener{conn: serverConn, closed: make(chan struct{})}
	connections := make(chan *client.Conn, 1)
	s := server.New(options.WithServerWriteTimeout(200*time.Millisecond), options.WithOnNewConn(func(cc *client.Conn) {
		connections <- cc
	}))
	served := make(chan error, 1)
	go func() { served <- s.Serve(listener) }()
	defer func() {
		close(packet.release)
		s.Stop()
		require.NoError(t, <-served)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, clientConn.HandshakeContext(ctx))
	var conn *client.Conn
	select {
	case conn = <-connections:
	case <-ctx.Done():
		t.Fatal("server handshake did not finish")
	}
	packet.block.Store(true)
	req := pool.NewMessage(context.Background())
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.Content)
	req.SetMessageID(42)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- conn.WriteMessage(req) }()
	select {
	case <-packet.started:
	case <-ctx.Done():
		t.Fatal("DTLS socket write did not start")
	}
	select {
	case err = <-done:
		require.ErrorIs(t, err, coapNet.ErrWriteTimeout)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), time.Second)
	case <-time.After(time.Second):
		t.Fatal("DTLS write did not time out")
	}
	err = conn.WriteMessage(req)
	require.ErrorIs(t, err, coapNet.ErrConnectionIsClosed)
}
