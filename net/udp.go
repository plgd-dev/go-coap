package net

import (
	"net"
)

// ConnUDPContext holds the remote address and the associated
// out-of-band data.
type ConnUDPContext struct {
	raddr *net.UDPAddr
}

// NewConnUDPContext creates conn udp context.
func NewConnUDPContext(raddr *net.UDPAddr) *ConnUDPContext {
	return &ConnUDPContext{
		raddr: raddr,
	}
}

// RemoteAddr returns the remote network address.
func (s *ConnUDPContext) RemoteAddr() net.Addr { return s.raddr }

// Key returns the key session for the map using
func (s *ConnUDPContext) Key() string {
	key := s.RemoteAddr().String()
	return key
}

// ReadFromSessionUDP acts just like net.UDPConn.ReadFrom(), but returns a session object instead of a
// net.UDPAddr.
func ReadFromSessionUDP(conn *net.UDPConn, b []byte) (int, *ConnUDPContext, error) {
	n, raddr, err := conn.ReadFromUDP(b)
	if err != nil {
		return n, nil, err
	}
	return n, &ConnUDPContext{raddr}, err
}

// WriteToSessionUDP acts just like net.UDPConn.WriteTo(), but uses a *SessionUDP instead of a net.Addr.
func WriteToSessionUDP(conn *net.UDPConn, session *ConnUDPContext, b []byte) (int, error) {
	if conn.RemoteAddr() == nil {
		// Connection remote address must be nil otherwise
		// "WriteTo with pre-connected connection" will be thrown
		return conn.WriteToUDP(b, session.raddr)
	}
	return conn.Write(b)

}
