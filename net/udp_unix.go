// +build !windows

package net

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// SetUDPSocketOptions set controls FlagDst,FlagInterface to UDPConn.
func SetUDPSocketOptions(conn *net.UDPConn) error {
	if ip4 := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip4 != nil {
		return ipv4.NewPacketConn(conn).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	}
	return ipv6.NewPacketConn(conn).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
}
