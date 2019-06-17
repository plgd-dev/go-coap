package net

import (
	"encoding/base64"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// This is the required size of the OOB buffer to pass to ReadMsgUDP.
var udpOOBSize = func() int {
	// We can't know whether we'll get an IPv4 control message or an
	// IPv6 control message ahead of time. To get around this, we size
	// the buffer equal to the largest of the two.

	oob4 := ipv4.NewControlMessage(ipv4.FlagDst | ipv4.FlagInterface)
	oob6 := ipv6.NewControlMessage(ipv6.FlagDst | ipv6.FlagInterface)

	if len(oob4) > len(oob6) {
		return len(oob4)
	}

	return len(oob6)
}()

// ConnUDPContext holds the remote address and the associated
// out-of-band data.
type ConnUDPContext struct {
	raddr   *net.UDPAddr
	context []byte
}

// NewConnUDPContext creates conn udp context.
func NewConnUDPContext(raddr *net.UDPAddr, oob []byte) *ConnUDPContext {
	return &ConnUDPContext{
		raddr:   raddr,
		context: oob,
	}
}

// RemoteAddr returns the remote network address.
func (s *ConnUDPContext) RemoteAddr() net.Addr { return s.raddr }

// Key returns the key session for the map using
func (s *ConnUDPContext) Key() string {
	key := s.RemoteAddr().String() + "-" + base64.StdEncoding.EncodeToString(s.context)
	return key
}

// ReadFromSessionUDP acts just like net.UDPConn.ReadFrom(), but returns a session object instead of a
// net.UDPAddr.
func ReadFromSessionUDP(conn *net.UDPConn, b []byte) (int, *ConnUDPContext, error) {
	oob := make([]byte, udpOOBSize)
	n, oobn, _, raddr, err := conn.ReadMsgUDP(b, oob)
	if err != nil {
		return n, nil, err
	}
	return n, &ConnUDPContext{raddr, oob[:oobn]}, err
}

// WriteToSessionUDP acts just like net.UDPConn.WriteTo(), but uses a *SessionUDP instead of a net.Addr.
func WriteToSessionUDP(conn *net.UDPConn, session *ConnUDPContext, b []byte) (int, error) {
	//check if socket is connected via Dial
	if conn.RemoteAddr() == nil {
		return conn.WriteToUDP(b, session.raddr)
	}

	n, _, err := conn.WriteMsgUDP(b, correctSource(session.context), nil)
	return n, err
}

// parseDstFromOOB takes oob data and returns the destination IP.
func parseDstFromOOB(oob []byte) net.IP {
	// Start with IPv6 and then fallback to IPv4
	// TODO(fastest963): Figure out a way to prefer one or the other. Looking at
	// the lvl of the header for a 0 or 41 isn't cross-platform.
	cm6 := new(ipv6.ControlMessage)
	if cm6.Parse(oob) == nil && cm6.Dst != nil {
		return cm6.Dst
	}
	cm4 := new(ipv4.ControlMessage)
	if cm4.Parse(oob) == nil && cm4.Dst != nil {
		return cm4.Dst
	}
	return nil
}
