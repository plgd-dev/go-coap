package server

import (
	"net"
	"testing"

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
