package coap

import (
	"encoding/base64"
	"net"
	"runtime"

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

// SessionUDPData holds the remote address and the associated
// out-of-band data.
type SessionUDPData struct {
	raddr   *net.UDPAddr
	context []byte
}

// RemoteAddr returns the remote network address.
func (s *SessionUDPData) RemoteAddr() net.Addr { return s.raddr }

// Key returns the key session for the map using
func (s *SessionUDPData) Key() string {
	key := s.RemoteAddr().String() + "-" + base64.StdEncoding.EncodeToString(s.context)
	return key
}

// ReadFromSessionUDP acts just like net.UDPConn.ReadFrom(), but returns a session object instead of a
// net.UDPAddr.
func ReadFromSessionUDP(conn *net.UDPConn, b []byte) (int, *SessionUDPData, error) {
	oob := make([]byte, udpOOBSize)
	n, oobn, _, raddr, err := conn.ReadMsgUDP(b, oob)
	if err != nil {
		return n, nil, err
	}
	return n, &SessionUDPData{raddr, oob[:oobn]}, err
}

// WriteToSessionUDP acts just like net.UDPConn.WriteTo(), but uses a *SessionUDP instead of a net.Addr.
func WriteToSessionUDP(conn *net.UDPConn, b []byte, session *SessionUDPData) (int, error) {

	//log.Printf("sending %v to %v", b, session)

	//check if socket is connected via Dial
	if conn.RemoteAddr() == nil {
		return conn.WriteToUDP(b, session.raddr)
	}

	n, _, err := conn.WriteMsgUDP(b, correctSource(session.context), nil)
	return n, err
}

func setUDPSocketOptions(conn *net.UDPConn) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if ip4 := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip4 != nil {
		return ipv4.NewPacketConn(conn).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	}
	return ipv6.NewPacketConn(conn).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
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

// correctSource takes oob data and returns new oob data with the Src equal to the Dst
func correctSource(oob []byte) []byte {
	if runtime.GOOS == "windows" {
		return oob
	}
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

func joinGroup(conn *net.UDPConn, ifi *net.Interface, gaddr *net.UDPAddr) error {
	if ip4 := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip4 != nil {
		return ipv4.NewPacketConn(conn).JoinGroup(ifi, gaddr)
	}
	return ipv6.NewPacketConn(conn).JoinGroup(ifi, gaddr)
}
