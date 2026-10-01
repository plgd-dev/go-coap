package qblocktransport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/stretchr/testify/require"
)

// This catches bypassing directional relay decisions or leaving relay readers
// alive after Close. Both sides carry complete literal CoAP UDP datagrams.
func TestUDPRelayFaultsAndShutdown(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	c := NewController()
	require.NoError(t, c.Arm([]qblocklink.Rule{
		{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 1, Action: qblocklink.Drop},
		{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Duplicate},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Duplicate},
	}))
	relay, err := NewUDPRelay(server.LocalAddr().String(), c)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = relay.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, relay.Wait(ctx))
	})
	peer, err := net.DialUDP("udp4", nil, relay.Addr().(*net.UDPAddr))
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	require.NoError(t, server.SetDeadline(time.Now().Add(time.Second)))
	require.NoError(t, peer.SetDeadline(time.Now().Add(time.Second)))
	_, err = peer.Write([]byte{0x50, 2, 0, 1, 0xd0, 6})
	require.NoError(t, err)
	q1 := []byte{0x50, 2, 0, 2, 0xd0, 6}
	_, err = peer.Write(q1)
	require.NoError(t, err)
	buf := make([]byte, 100)
	var upstream *net.UDPAddr
	for i := 0; i < 2; i++ {
		n, addr, err := server.ReadFromUDP(buf)
		require.NoError(t, err)
		require.Equal(t, q1, buf[:n])
		upstream = addr
	}
	q2 := []byte{0x50, 69, 0, 3, 0xd0, 18}
	_, err = server.WriteToUDP(q2, upstream)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		n, err := peer.Read(buf)
		require.NoError(t, err)
		require.Equal(t, q2, buf[:n])
	}
	require.Len(t, c.Snapshot().Faults, 3)
	require.Equal(t, []Forward{{"faults", qblocklink.ClientToServer, 2, 2}, {"faults", qblocklink.ServerToClient, 3, 2}}, c.Snapshot().Forwarded)
	AssertForwarded(t, c.Snapshot())
}
