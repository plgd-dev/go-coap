package net

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// ConnUDP is a udp connection provides Read/Write with context.
//
// Multiple goroutines may invoke methods on a ConnUDP simultaneously.
type ConnUDP struct {
	heartBeat         time.Duration
	connection        *net.UDPConn
	packetConn        packetConn
	multicastHopLimit int
	errors            func(err error)

	lock sync.Mutex
}

type ControlMessage struct {
	Src     net.IP // source address, specifying only
	IfIndex int    // interface index, must be 1 <= value when specifying
}

type packetConn interface {
	SetWriteDeadline(t time.Time) error
	WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error)
	SetMulticastInterface(ifi *net.Interface) error
	SetMulticastHopLimit(hoplim int) error
	SetMulticastLoopback(on bool) error
	JoinGroup(ifi *net.Interface, group net.Addr) error
	LeaveGroup(ifi *net.Interface, group net.Addr) error
}

type packetConnIPv4 struct {
	packetConnIPv4 *ipv4.PacketConn
}

func newPacketConnIPv4(p *ipv4.PacketConn) *packetConnIPv4 {
	return &packetConnIPv4{p}
}

func (p *packetConnIPv4) SetMulticastInterface(ifi *net.Interface) error {
	return p.packetConnIPv4.SetMulticastInterface(ifi)
}

func (p *packetConnIPv4) SetWriteDeadline(t time.Time) error {
	return p.packetConnIPv4.SetWriteDeadline(t)
}

func (p *packetConnIPv4) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error) {
	var c *ipv4.ControlMessage
	if cm != nil {
		c = &ipv4.ControlMessage{
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return p.packetConnIPv4.WriteTo(b, c, dst)
}

func (p *packetConnIPv4) SetMulticastHopLimit(hoplim int) error {
	return p.packetConnIPv4.SetMulticastTTL(hoplim)
}

func (p *packetConnIPv4) SetMulticastLoopback(on bool) error {
	return p.packetConnIPv4.SetMulticastLoopback(on)
}

func (p *packetConnIPv4) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConnIPv4.JoinGroup(ifi, group)
}

func (p *packetConnIPv4) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConnIPv4.LeaveGroup(ifi, group)
}

type packetConnIPv6 struct {
	packetConnIPv6 *ipv6.PacketConn
}

func newPacketConnIPv6(p *ipv6.PacketConn) *packetConnIPv6 {
	return &packetConnIPv6{p}
}

func (p *packetConnIPv6) SetMulticastInterface(ifi *net.Interface) error {
	return p.packetConnIPv6.SetMulticastInterface(ifi)
}

func (p *packetConnIPv6) SetWriteDeadline(t time.Time) error {
	return p.packetConnIPv6.SetWriteDeadline(t)
}

func (p *packetConnIPv6) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error) {
	var c *ipv6.ControlMessage
	if cm != nil {
		c = &ipv6.ControlMessage{
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return p.packetConnIPv6.WriteTo(b, c, dst)
}

func (p *packetConnIPv6) SetMulticastHopLimit(hoplim int) error {
	return p.packetConnIPv6.SetMulticastHopLimit(hoplim)
}

func (p *packetConnIPv6) SetMulticastLoopback(on bool) error {
	return p.packetConnIPv6.SetMulticastLoopback(on)
}

func (p *packetConnIPv6) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConnIPv6.JoinGroup(ifi, group)
}

func (p *packetConnIPv6) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConnIPv6.LeaveGroup(ifi, group)
}

func (p *packetConnIPv6) SetControlMessage(on bool) error {
	return p.packetConnIPv6.SetMulticastLoopback(on)
}

func isIPv6(addr net.IP) bool {
	if ip := addr.To16(); ip != nil && ip.To4() == nil {
		return true
	}
	return false
}

// NewConnUDP creates connection over net.UDPConn.
func NewConnUDP(c *net.UDPConn, heartBeat time.Duration, multicastHopLimit int, errors func(err error)) *ConnUDP {
	var packetConn packetConn

	if isIPv6(c.LocalAddr().(*net.UDPAddr).IP) {
		packetConn = newPacketConnIPv6(ipv6.NewPacketConn(c))
	} else {
		packetConn = newPacketConnIPv4(ipv4.NewPacketConn(c))
	}

	connection := ConnUDP{connection: c, heartBeat: heartBeat, packetConn: packetConn, multicastHopLimit: multicastHopLimit, errors: errors}
	return &connection
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (c *ConnUDP) LocalAddr() net.Addr {
	return c.connection.LocalAddr()
}

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (c *ConnUDP) RemoteAddr() net.Addr {
	return c.connection.RemoteAddr()
}

// Close closes the connection.
func (c *ConnUDP) Close() error {
	return c.connection.Close()
}

func (c *ConnUDP) writeToAddr(ctx context.Context, heartBeat time.Duration, multicastHopLimit int, iface net.Interface, srcAddr net.Addr, port string, udpCtx *ConnUDPContext, buffer []byte) error {
	netType := "udp4"
	if isIPv6(udpCtx.raddr.IP) {
		netType = "udp6"
	}
	addrMask := srcAddr.String()
	addr := strings.Split(addrMask, "/")[0]
	if strings.Contains(addr, ":") && netType == "udp4" {
		return nil
	}
	if !strings.Contains(addr, ":") && netType == "udp6" {
		return nil
	}
	var p packetConn
	if netType == "udp4" {
		p = newPacketConnIPv4(ipv4.NewPacketConn(c.connection))
	} else {
		p = newPacketConnIPv6(ipv6.NewPacketConn(c.connection))
	}

	if err := p.SetMulticastInterface(&iface); err != nil {
		return err
	}
	p.SetMulticastHopLimit(multicastHopLimit)
	err := p.SetWriteDeadline(time.Now().Add(heartBeat))
	if err != nil {
		return fmt.Errorf("cannot write multicast with context: cannot set write deadline for connection: %v", err)
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return fmt.Errorf("cannot parse ip (%v) for iface %v", ip, iface.Name)
	}
	_, err = p.WriteTo(buffer, &ControlMessage{
		Src:     ip,
		IfIndex: iface.Index,
	}, udpCtx.raddr)
	return err
}

func (c *ConnUDP) writeMulticastWithContext(ctx context.Context, udpCtx *ConnUDPContext, buffer []byte) error {
	if udpCtx == nil {
		return fmt.Errorf("cannot write multicast with context: invalid udpCtx")
	}
	if _, ok := c.packetConn.(*packetConnIPv4); ok && isIPv6(udpCtx.raddr.IP) {
		return fmt.Errorf("cannot write multicast with context: invalid destination address")
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("cannot write multicast with context: cannot get interfaces for multicast connection: %v", err)
	}
	c.lock.Lock()
	defer c.lock.Unlock()
LOOP:
	for _, iface := range ifaces {
		ifaceAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		if len(ifaceAddrs) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		addr := strings.Split(c.connection.LocalAddr().String(), ":")
		port := addr[len(addr)-1]

		for _, ifaceAddr := range ifaceAddrs {
			err = c.writeToAddr(ctx, c.heartBeat, c.multicastHopLimit, iface, ifaceAddr, port, udpCtx, buffer)
			if err != nil {
				if isTemporary(err) {
					continue LOOP
				}
				if c.errors != nil {
					c.errors(fmt.Errorf("cannot write multicast to %v: %w", iface.Name, err))
				}
			}
		}
	}
	return nil
}

// WriteWithContext writes data with context.
func (c *ConnUDP) WriteWithContext(ctx context.Context, udpCtx *ConnUDPContext, buffer []byte) error {
	if udpCtx == nil {
		return fmt.Errorf("cannot write with context: invalid udpCtx")
	}
	if udpCtx.raddr.IP.IsMulticast() {
		return c.writeMulticastWithContext(ctx, udpCtx, buffer)
	}

	written := 0
	c.lock.Lock()
	defer c.lock.Unlock()
	for written < len(buffer) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		err := c.connection.SetWriteDeadline(time.Now().Add(c.heartBeat))
		if err != nil {
			return fmt.Errorf("cannot set write deadline for udp connection: %v", err)
		}
		n, err := WriteToSessionUDP(c.connection, udpCtx, buffer[written:])
		if err != nil {
			if isTemporary(err) {
				continue
			}
			return fmt.Errorf("cannot write to udp connection: %v", err)
		}
		written += n
	}

	return nil
}

// ReadWithContext reads packet with context.
func (c *ConnUDP) ReadWithContext(ctx context.Context, buffer []byte) (int, *ConnUDPContext, error) {
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() != nil {
				return -1, nil, fmt.Errorf("cannot read from udp connection: %v", ctx.Err())
			}
			return -1, nil, fmt.Errorf("cannot read from udp connection")
		default:
		}

		err := c.connection.SetReadDeadline(time.Now().Add(c.heartBeat))
		if err != nil {
			return -1, nil, fmt.Errorf("cannot set read deadline for udp connection: %v", err)
		}
		n, s, err := ReadFromSessionUDP(c.connection, buffer)
		if err != nil {
			if isTemporary(err) {
				continue
			}
			return -1, nil, fmt.Errorf("cannot read from udp connection: %v", ctx.Err())
		}
		return n, s, err
	}
}

// SetMulticastLoopback sets whether transmitted multicast packets
// should be copied and send back to the originator.
func (c *ConnUDP) SetMulticastLoopback(on bool) error {
	return c.packetConn.SetMulticastLoopback(on)
}

// JoinGroup joins the group address group on the interface ifi.
// By default all sources that can cast data to group are accepted.
// It's possible to mute and unmute data transmission from a specific
// source by using ExcludeSourceSpecificGroup and
// IncludeSourceSpecificGroup.
// JoinGroup uses the system assigned multicast interface when ifi is
// nil, although this is not recommended because the assignment
// depends on platforms and sometimes it might require routing
// configuration.
func (c *ConnUDP) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return c.packetConn.JoinGroup(ifi, group)
}

// LeaveGroup leaves the group address group on the interface ifi
// regardless of whether the group is any-source group or source-specific group.
func (c *ConnUDP) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return c.packetConn.LeaveGroup(ifi, group)
}
