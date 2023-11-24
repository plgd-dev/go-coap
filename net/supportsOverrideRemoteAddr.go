package net

import "net"

func supportsOverrideRemoteAddr(c *net.UDPConn) bool {
	return c.RemoteAddr() == nil
}
