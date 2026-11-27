package server

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestMalformedResetWithDisabledWriteTimeout(t *testing.T) {
	s := New()
	defer s.Stop()
	s.cfg.WriteTimeout = 0
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	cc, created, err := s.getConn(listener, peer.LocalAddr().(*net.UDPAddr), listener.LocalAddr().(*net.UDPAddr), true)
	require.NoError(t, err)
	require.True(t, created)
	defer getClose(cc)()
	s.maybeRejectMalformedConfirmable(listener, cc, nil, &client.MalformedMessageError{
		Type: message.Confirmable, MessageID: 42, HasHeader: true, Err: errors.New("malformed"),
	})
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 16)
	n, _, err := peer.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Equal(t, []byte{0x70, 0x00, 0x00, 0x2a}, buf[:n])
}
