//go:build windows

package net

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
)

// TestWindowsWriteMsgUDPReportingBehavior documents and pins the Windows-specific UDP write
// behavior that motivated the blockwise fix.
//
// Background: on Windows (observed with go1.26.4) (*net.UDPConn).WriteMsgUDP delivers the
// datagram correctly but reports n=0, err=nil. go-coap's unconnected server-listener path
// uses WriteMsgUDP, so writeWithCfg previously treated every server response as a partial
// write (ErrWriteInterrupted) and blockwise transfers looped forever.
//
// The fix keeps the portable WriteMsgUDP path (it is dual-stack capable, unlike
// ipv4.PacketConn.WriteTo which fails on macOS) and instead hardens writeWithCfg to treat a
// zero-length report as success. This test acts as an early-warning regression guard:
//   - It does NOT hard-fail on the raw stdlib WriteMsgUDP behavior (a future Go fix may
//     change n from 0 to the real length). Instead it logs what WriteMsgUDP reports so a
//     change is visible in test output.
//   - It DOES hard-assert that the public write path (WriteWithContext / writeWithCfg)
//     succeeds and the datagram is delivered, even though the raw WriteMsgUDP reports n=0.
//     This is the guarantee the fix relies on; if it regresses the test fails.
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

		// 1) Raw stdlib WriteMsgUDP: only logged, not asserted. On Windows it historically
		//    reports n=0 even though the datagram is delivered. A change here (e.g. a future
		//    Go fix) becomes visible in the test output without failing the suite.
		rawN, _, errRaw := server.connection.WriteMsgUDP(payload, nil, clientAddr)
		require.NoError(t, errRaw)
		drainOne(t, client, size)
		t.Logf("size=%4d  raw WriteMsgUDP reported n=%d (Windows historically reports 0 despite delivery)", size, rawN)

		// 2) Informational: ipv4.PacketConn.WriteTo reports the full length on Windows. We do
		//    NOT use it as the write path because it is not dual-stack capable on macOS, but it
		//    illustrates that the n=0 quirk is specific to WriteMsgUDP.
		p := ipv4.NewPacketConn(server.connection)
		wtN, errWt := p.WriteTo(payload, &ipv4.ControlMessage{Src: net.IPv4(127, 0, 0, 1)}, clientAddr)
		require.NoError(t, errWt)
		require.Equal(t, size, wtN, "ipv4.PacketConn.WriteTo must report full length on Windows")
		drainOne(t, client, size)

		// 3) The real guarantee: the public write path must succeed and deliver the datagram
		//    on Windows even though the underlying WriteMsgUDP reports n=0. This is what the
		//    writeWithCfg hardening ensures.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
		errPub := server.WriteWithContext(ctx, clientAddr, payload)
		cancel()
		require.NoError(t, errPub, "WriteWithContext must not return ErrWriteInterrupted despite raw n=0")
		drainOne(t, client, size)
	}
}

func TestNormalizeWriteMsgUDPResult(t *testing.T) {
	gotN, gotErr := normalizeWriteMsgUDPResult(0, nil, []byte("abc"))
	require.NoError(t, gotErr)
	require.Equal(t, 3, gotN)

	gotN, gotErr = normalizeWriteMsgUDPResult(5, nil, []byte("abc"))
	require.NoError(t, gotErr)
	require.Equal(t, 5, gotN)

	boom := context.DeadlineExceeded
	gotN, gotErr = normalizeWriteMsgUDPResult(0, boom, []byte("abc"))
	require.ErrorIs(t, gotErr, boom)
	require.Equal(t, 0, gotN)
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
