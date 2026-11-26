package server

import (
	"net"
	"testing"

	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/stretchr/testify/require"
)

func TestGetConnKeyIgnoresMulticastLocalAddress(t *testing.T) {
	raddr := &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 56830}
	mcastV6 := &net.UDPAddr{IP: net.ParseIP("ff02::fd"), Port: 5683}
	mcastV4 := &net.UDPAddr{IP: net.ParseIP("224.0.1.187"), Port: 5683}
	normalized := &net.UDPAddr{Port: 5683}

	require.Equal(t, getConnKey(raddr, mcastV6), getConnKey(raddr, normalized))
	require.Equal(t, getConnKey(raddr, mcastV4), getConnKey(raddr, normalized))
}

func TestGetConnKeyKeepsUnicastLocalAddressDistinct(t *testing.T) {
	raddr := &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 56830}
	laddrA := &net.UDPAddr{IP: net.ParseIP("2001:db8::10"), Port: 5683}
	laddrB := &net.UDPAddr{IP: net.ParseIP("2001:db8::11"), Port: 5683}

	require.NotEqual(t, getConnKey(raddr, laddrA), getConnKey(raddr, laddrB))
}

func TestGetConnKeyNormalizesUnspecifiedLocalAddress(t *testing.T) {
	raddr := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 56830}
	unspecifiedV4 := &net.UDPAddr{IP: net.IPv4zero, Port: 5683}
	unspecifiedV6 := &net.UDPAddr{IP: net.IPv6zero, Port: 5683}
	normalized := &net.UDPAddr{Port: 5683}

	require.Equal(t, getConnKey(raddr, unspecifiedV4), getConnKey(raddr, normalized))
	require.Equal(t, getConnKey(raddr, unspecifiedV6), getConnKey(raddr, normalized))
}

func TestEndpointKeyNormalizesIPv4AndPreservesIPv6Zone(t *testing.T) {
	ipv4 := &net.UDPAddr{IP: net.IP{192, 0, 2, 1}, Port: 5683}
	mapped := &net.UDPAddr{IP: net.ParseIP("::ffff:192.0.2.1"), Port: 5683, Zone: "ignored"}
	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	require.Equal(t, getEndpointKey(ipv4), getEndpointKey(mapped))
	require.Equal(t, getConnKey(ipv4, local), getConnKey(mapped, local))
	require.NotEqual(t, getEndpointKey(ipv4), getEndpointKey(&net.UDPAddr{IP: ipv4.IP, Port: 5684}))

	zone1 := &net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 5683, Zone: "eth0"}
	zone2 := &net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 5683, Zone: "eth1"}
	require.NotEqual(t, getEndpointKey(zone1), getEndpointKey(zone2))
}

func TestNormalizedEndpointSharesSequenceAcrossConnections(t *testing.T) {
	s := New(endpointMessageIDLimitOption(1))
	defer s.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	ipv4 := &net.UDPAddr{IP: net.IP{192, 0, 2, 1}, Port: 5683}
	mapped := &net.UDPAddr{IP: net.ParseIP("::ffff:192.0.2.1"), Port: 5683, Zone: "ignored"}
	local1 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	local2 := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
	first, created, err := s.getOrCreateConn(listener, ipv4, local1)
	require.NoError(t, err)
	require.True(t, created)
	last := first.GetMessageID()
	second, created, err := s.getOrCreateConn(listener, mapped, local2)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, (last+1)&0xffff, second.GetMessageID())
	getClose(first)()
	getClose(second)()
	reconnected, created, err := s.getOrCreateConn(listener, mapped, local1)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, (last+2)&0xffff, reconnected.GetMessageID())
	getClose(reconnected)()
}
