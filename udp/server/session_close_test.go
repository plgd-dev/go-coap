package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/stretchr/testify/require"
)

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
