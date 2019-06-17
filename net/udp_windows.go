package net

import (
	"net"
)

// windows specific functions for udp

// SetUDPSocketOptions set controls FlagDst,FlagInterface to UDPConn - not supported by windows.
func SetUDPSocketOptions(conn *net.UDPConn) error {
	return nil
}

// correctSource takes oob data and returns new oob data with the Src equal to the Dst
func correctSource(oob []byte) []byte {
	return oob
}
