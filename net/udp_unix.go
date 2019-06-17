// +build !windows

package net

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// correctSource takes oob data and returns new oob data with the Src equal to the Dst
func correctSource(oob []byte) []byte {
	dst := parseDstFromOOB(oob)
	if dst == nil {
		return nil
	}
	// If the dst is definitely an IPv6, then use ipv6's ControlMessage to
	// respond otherwise use ipv4's because ipv6's marshal ignores ipv4
	// addresses.
	if dst.To4() == nil {
		cm := new(ipv6.ControlMessage)
		cm.Src = dst
		oob = cm.Marshal()
	} else {
		cm := new(ipv4.ControlMessage)
		cm.Src = dst
		oob = cm.Marshal()
	}
	return oob
}

// SetUDPSocketOptions set controls FlagDst,FlagInterface to UDPConn.
func SetUDPSocketOptions(conn *net.UDPConn) error {
	if ip4 := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip4 != nil {
		return ipv4.NewPacketConn(conn).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	}
	return ipv6.NewPacketConn(conn).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
}
