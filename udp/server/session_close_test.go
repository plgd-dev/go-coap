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

type blockingMessageBody struct {
	started chan struct{}
	release chan struct{}
	read    bool
}

func (b *blockingMessageBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	close(b.started)
	<-b.release
	p[0] = 1
	return 1, nil
}

func (*blockingMessageBody) Seek(_ int64, whence int) (int64, error) {
	if whence == io.SeekEnd {
		return 1, nil
	}
	return 0, nil
}

func TestSessionCloseDoesNotWaitForMessageMarshalling(t *testing.T) {
	for _, multicast := range []bool{false, true} {
		t.Run(fmt.Sprintf("multicast=%t", multicast), func(t *testing.T) {
			listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			s := NewSession(context.Background(), context.Background(), listener, listener.LocalAddr().(*net.UDPAddr), 1024, 1024, false)
			messages := pool.New(0, 0)
			req := messages.AcquireMessage(context.Background())
			defer messages.ReleaseMessage(req)
			req.SetType(message.NonConfirmable)
			req.SetCode(codes.POST)
			req.SetMessageID(42)
			body := &blockingMessageBody{started: make(chan struct{}), release: make(chan struct{})}
			req.SetBody(body)
			written := make(chan error, 1)
			go func() {
				if multicast {
					written <- s.WriteMulticastMessage(req, &net.UDPAddr{IP: net.IPv4(224, 0, 1, 187), Port: 5683})
				} else {
					written <- s.WriteMessage(req)
				}
			}()
			<-body.started
			closed := make(chan error, 1)
			go func() { closed <- s.Close() }()
			select {
			case err := <-closed:
				require.NoError(t, err)
			case <-time.After(time.Second):
				close(body.release)
				t.Logf("write returned: %v", <-written)
				t.Fatal("close waited for message body")
			}
			close(body.release)
			select {
			case err := <-written:
				require.ErrorIs(t, err, net.ErrClosed)
			case <-time.After(time.Second):
				t.Fatal("write did not finish")
			}
		})
	}
}

func TestSessionWriteContextTimeoutAndClose(t *testing.T) {
	s := NewSession(context.Background(), context.Background(), nil, nil, 1024, 1024, false)
	s.writeTimeout = 30 * time.Millisecond
	ctx, cancel := s.writeContext(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("write context did not time out")
	}
	ctx, cancel = s.writeContext(context.Background())
	defer cancel()
	require.NoError(t, s.Close())
	select {
	case <-ctx.Done():
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("write context was not canceled by close")
	}
}

func TestSessionCloseWaitsForWritesAndRejectsLaterWrites(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	s := NewSession(context.Background(), context.Background(), listener, listener.LocalAddr().(*net.UDPAddr), 1024, 1024, false)
	s.writeMu.RLock()
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case <-s.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("session was not cancelled")
	}
	select {
	case closeErr := <-closed:
		t.Fatalf("session closed before its write completed: %v", closeErr)
	default:
	}
	s.writeMu.RUnlock()
	select {
	case err = <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("session did not close")
	}
	messages := pool.New(0, 0)
	req := messages.AcquireMessage(context.Background())
	defer messages.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(42)
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
	case callbackErr := <-callbacks:
		require.NoError(t, callbackErr)
	case writeErr := <-written:
		t.Fatalf("multicast returned before callback: %v", writeErr)
	case <-time.After(time.Second):
		t.Fatal("multicast interface error callback did not complete")
	}
	select {
	case writeErr := <-written:
		require.NoError(t, writeErr)
	case <-time.After(time.Second):
		t.Fatal("multicast write did not finish")
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
