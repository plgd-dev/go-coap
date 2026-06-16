//go:build windows

package net

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
)

// TestWindowsWriteMsgUDPReportingBehavior documents and pins the Windows-specific UDP write
// behavior that motivated routing writeTo through the ipv4/ipv6 PacketConn.WriteTo wrapper.
//
// Background: on Windows (observed with go1.26.4) (*net.UDPConn).WriteMsgUDP delivers the
// datagram correctly but reports n=0, err=nil. go-coap previously used WriteMsgUDP for the
// unconnected server-listener path, so writeWithCfg treated every server response as a
// partial write (ErrWriteInterrupted) and blockwise transfers looped forever.
//
// This test acts as an early-warning regression guard:
//   - It does NOT hard-fail on the stdlib WriteMsgUDP behavior (a future Go fix may change
//     n from 0 to the real length). Instead it logs what WriteMsgUDP reports so a change is
//     visible in test output.
//   - It DOES hard-assert that ipv4.PacketConn.WriteTo and go-coap's writeTo report the full
//     byte count on Windows. These are the guarantees the fix relies on; if they regress the
//     test fails.
func TestWindowsWriteMsgUDPReportingBehavior(t *testing.T) {
	server, err := NewListenUDP(udp4Network, "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		errC := server.Close()
		require.NoError(t, errC)
	}()
	require.True(t, supportsOverrideRemoteAddr(server.connection))

	serverAddr, ok := server.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	client, err := net.DialUDP(udp4Network, nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverAddr.Port})
	require.NoError(t, err)
	defer func() {
		errC := client.Close()
		require.NoError(t, errC)
	}()
	clientAddr, ok := client.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	sizes := []int{300, 1024, 1100, 2000}
	for _, size := range sizes {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(i % 251)
		}

		// 1) Raw stdlib WriteMsgUDP: only logged, not asserted (may report n=0 on Windows).
		rawN, _, errRaw := server.connection.WriteMsgUDP(payload, nil, clientAddr)
		require.NoError(t, errRaw)
		drainOne(t, client, size)
		t.Logf("size=%4d  WriteMsgUDP reported n=%d (Windows historically reports 0 despite delivery)", size, rawN)

		// 2) ipv4.PacketConn.WriteTo: must report the full length on Windows (the working path).
		p := ipv4.NewPacketConn(server.connection)
		wtN, errWt := p.WriteTo(payload, &ipv4.ControlMessage{Src: net.IPv4(127, 0, 0, 1)}, clientAddr)
		require.NoError(t, errWt)
		require.Equal(t, size, wtN, "ipv4.PacketConn.WriteTo must report full length on Windows")
		drainOne(t, client, size)

		// 3) go-coap writeTo (the fixed path): must report the full length on Windows.
		gcN, errGc := server.writeTo(clientAddr, nil, payload)
		require.NoError(t, errGc)
		require.Equal(t, size, gcN, "writeTo must report full length on Windows after the fix")
		drainOne(t, client, size)
	}
}

func drainOne(t *testing.T, client *net.UDPConn, size int) {
	t.Helper()
	errR := client.SetReadDeadline(time.Now().Add(time.Second))
	require.NoError(t, errR)
	buf := make([]byte, size+16)
	n, err := client.Read(buf)
	require.NoError(t, err)
	require.Equal(t, size, n)
}
