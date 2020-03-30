package net

import (
	"net"
)

// windows specific functions for udp

// SetUDPSocketOptions set controls FlagDst,FlagInterface to UDPConn - not supported by windows.
func SetUDPSocketOptions(conn *net.UDPConn) error {
	return nil
}
