package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/stretchr/testify/require"
)

type blockingCloseTestBody struct {
	started chan struct{}
	release chan struct{}
	read    bool
}

func (b *blockingCloseTestBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	close(b.started)
	<-b.release
	p[0] = 1
	return 1, nil
}

func (*blockingCloseTestBody) Seek(_ int64, whence int) (int64, error) {
	if whence == io.SeekEnd {
		return 1, nil
	}
	return 0, nil
}

func TestSessionCloseDoesNotWaitForWritesAndRejectsLaterWrites(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	s := NewSession(context.Background(), context.Background(), listener, listener.LocalAddr().(*net.UDPAddr), 1024, 1024, false)
	messages := pool.New(0, 0)
	req := messages.AcquireMessage(context.Background())
	defer messages.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.POST)
	req.SetMessageID(42)
	body := &blockingCloseTestBody{started: make(chan struct{}), release: make(chan struct{})}
	req.SetBody(body)
	written := make(chan error, 1)
	go func() { written <- s.WriteMessage(req) }()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		close(body.release)
		t.Fatal("write did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err = <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		close(body.release)
		t.Logf("pending write returned: %v", <-written)
		t.Fatal("session waited for the pending write")
	}
	close(body.release)
	select {
	case err = <-written:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("write did not finish")
	}
	require.ErrorIs(t, s.WriteMessage(req), net.ErrClosed)
	require.ErrorIs(t, s.WriteMulticastMessage(req, &net.UDPAddr{IP: net.IPv4(224, 0, 1, 187), Port: 5683}), net.ErrClosed)
}

func TestMulticastInterfaceErrorCallbackCanCloseSession(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	s := NewSession(context.Background(), context.Background(), listener, listener.LocalAddr().(*net.UDPAddr), 1024, 1024, false)
	messages := pool.New(0, 0)
	req := messages.AcquireMessage(context.Background())
	defer messages.ReleaseMessage(req)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(42)
	iface := net.Interface{Index: -1, Name: "nonexistent-interface"}
	callbacks := make(chan error, 1)
	written := make(chan error, 1)
	go func() {
		written <- s.WriteMulticastMessage(req, &net.UDPAddr{IP: net.IPv4(224, 0, 1, 187), Port: 5683},
			coapNet.WithMulticastInterface(iface),
			coapNet.WithMulticastSource(net.IPv4(127, 0, 0, 1)),
			coapNet.WithMulticastInterfaceError(func(got *net.Interface, writeErr error) {
				if got == nil || got.Index != iface.Index {
					callbacks <- fmt.Errorf("unexpected interface: %v", got)
					return
				}
				if writeErr == nil {
					callbacks <- fmt.Errorf("expected interface write error")
					return
				}
				callbacks <- s.Close()
			}),
		)
	}()
	select {
	case writeErr := <-written:
		require.NoError(t, writeErr)
	case <-time.After(time.Second):
		t.Fatal("multicast write did not finish")
	}
	select {
	case callbackErr := <-callbacks:
		require.NoError(t, callbackErr)
	default:
		t.Fatal("multicast interface error callback did not run")
	}
	require.ErrorIs(t, s.WriteMulticastMessage(req, &net.UDPAddr{IP: net.IPv4(224, 0, 1, 187), Port: 5683}), net.ErrClosed)
}

func TestMulticastCallbacksPreserveCallerOptions(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	s := NewSession(context.Background(), context.Background(), listener, listener.LocalAddr().(*net.UDPAddr), 1024, 1024, false)
	defer s.Close()
	messages := pool.New(0, 0)
	req := messages.AcquireMessage(context.Background())
	defer messages.ReleaseMessage(req)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(42)
	var firstCalls, lastCalls int
	opts := make([]coapNet.MulticastOption, 4, 5)
	opts[0] = coapNet.WithMulticastInterface(net.Interface{Index: -1, Name: "nonexistent-interface"})
	opts[1] = coapNet.WithMulticastSource(net.IPv4(127, 0, 0, 1))
	opts[2] = coapNet.WithMulticastInterfaceError(func(*net.Interface, error) { firstCalls++ })
	opts[3] = coapNet.WithMulticastInterfaceError(func(*net.Interface, error) { lastCalls++ })
	tail := opts[:cap(opts)]
	tail[4] = coapNet.WithMulticastHoplimit(37)
	address := &net.UDPAddr{IP: net.IPv4(224, 0, 1, 187), Port: 5683}
	require.NoError(t, s.WriteMulticastMessage(req, address, opts...))
	require.Equal(t, 0, firstCalls)
	require.Equal(t, 1, lastCalls)
	preserved := coapNet.DefaultMulticastOptions()
	preserved.Apply(tail[4])
	require.Equal(t, 37, preserved.HopLimit)
	require.NoError(t, s.WriteMulticastMessage(req, address, opts...))
	require.Equal(t, 2, lastCalls)
}
