package net

import (
	"context"
	"fmt"
	"github.com/plgd-dev/go-coap/v2/udp/generic"
	"net"
	"sync"
	"time"

	"go.uber.org/atomic"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// UDPConn is a udp connection provides Read/Write with context.
//
// Multiple goroutines may invoke methods on a UDPConn simultaneously.
type UDPConn struct {
	packetConn packetConn
	network    string
	connection *net.UDPConn
	errors     func(err error)
	closed     atomic.Bool

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

// IsIPv6 return's true if addr is IPV6.
func IsIPv6(addr net.IP) bool {
	if ip := addr.To16(); ip != nil && ip.To4() == nil {
		return true
	}
	return false
}

var defaultUDPConnOptions = udpConnOptions{
	errors: func(err error) {
		// don't log any error from fails for multicast requests
	},
}

type udpConnOptions struct {
	errors func(err error)
}

func NewListenUDP(network, addr string, opts ...UDPOption) (*UDPConn, error) {
	listenAddress, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP(network, listenAddress)
	if err != nil {
		return nil, err
	}
	return NewUDPConn(network, conn, opts...), nil
}

// NewUDPConn creates connection over net.UDPConn.
func NewUDPConn(network string, c *net.UDPConn, opts ...UDPOption) *UDPConn {
	cfg := defaultUDPConnOptions
	for _, o := range opts {
		o.applyUDP(&cfg)
	}

	var packetConn packetConn

	if IsIPv6(c.LocalAddr().(*net.UDPAddr).IP) {
		packetConn = newPacketConnIPv6(ipv6.NewPacketConn(c))
	} else {
		packetConn = newPacketConnIPv4(ipv4.NewPacketConn(c))
	}

	return &UDPConn{
		network:    network,
		connection: c,
		packetConn: packetConn,
		errors:     cfg.errors,
	}
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (c *UDPConn) LocalAddr() net.Addr {
	return c.connection.LocalAddr()
}

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (c *UDPConn) RemoteAddr() net.Addr {
	return c.connection.RemoteAddr()
}

// Network name of the network (for example, udp4, udp6, udp)
func (c *UDPConn) Network() string {
	return c.network
}

// Close closes the connection.
func (c *UDPConn) Close() error {
	if !c.closed.CAS(false, true) {
		return nil
	}
	return c.connection.Close()
}

func (c *UDPConn) writeToAddr(iface *net.Interface, src *net.IP, multicastHopLimit int, raddr *net.UDPAddr, buffer []byte) error {
	var pktSrc net.IP
	var p packetConn
	if IsIPv6(raddr.IP) {
		p = newPacketConnIPv6(ipv6.NewPacketConn(c.connection))
		pktSrc = net.IPv6zero
	} else {
		p = newPacketConnIPv4(ipv4.NewPacketConn(c.connection))
		pktSrc = net.IPv4zero
	}
	if src != nil {
		pktSrc = *src
	}

	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	if iface != nil {
		if err := p.SetMulticastInterface(iface); err != nil {
			return err
		}
	}
	if err := p.SetMulticastHopLimit(multicastHopLimit); err != nil {
		return err
	}

	var err error
	if iface != nil || src != nil {
		_, err = p.WriteTo(buffer, &ControlMessage{
			Src:     pktSrc,
			IfIndex: iface.Index,
		}, raddr)
	} else {
		_, err = p.WriteTo(buffer, nil, raddr)
	}
	return err
}

func (c *UDPConn) WriteMulticast(ctx context.Context, raddr *net.UDPAddr, opt generic.MulticastOptions, buffer []byte) error {
	if raddr == nil {
		return fmt.Errorf("cannot write multicast with context: invalid raddr")
	}
	if _, ok := c.packetConn.(*packetConnIPv4); ok && IsIPv6(raddr.IP) {
		return fmt.Errorf("cannot write multicast with context: invalid destination address")
	}

	var err error
	var ifname string

	switch opt.IFaceMode {
	case generic.MulticastAllInterface:
		// send multicast to all interfaces by recursively calling ourselves with each
		ifaces, err := net.Interfaces()
		if err != nil {
			return fmt.Errorf("cannot write multicast with context: cannot get interfaces for multicast connection: %w", err)
		}

		for _, iface := range ifaces {
			specificOpt := opt
			specificOpt.Iface = iface
			specificOpt.IFaceMode = generic.MulticastSpecificInterface
			err = c.WriteMulticast(ctx, raddr, specificOpt, buffer)
			if err != nil {
				return err
			}
		}
		return nil
	case generic.MulticastAnyInterface:
		ifname = "any"
		err = c.writeToAddr(nil, opt.Source, opt.HopLimit, raddr, buffer)
	case generic.MulticastSpecificInterface:
		ifname = opt.Iface.Name
		err = c.writeToAddr(&opt.Iface, opt.Source, opt.HopLimit, raddr, buffer)
	}
	if err != nil {
		if c.errors != nil {
			c.errors(fmt.Errorf("cannot write multicast to %v: %w", ifname, err))
		}
	}
	return nil
}

// WriteWithContext writes data with context.
func (c *UDPConn) WriteWithContext(ctx context.Context, raddr *net.UDPAddr, buffer []byte) error {
	if raddr == nil {
		return fmt.Errorf("cannot write with context: invalid raddr")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	n, err := WriteToUDP(c.connection, raddr, buffer)
	if err != nil {
		return err
	}
	if n != len(buffer) {
		return ErrWriteInterrupted
	}

	return nil
}

// ReadWithContext reads packet with context.
func (c *UDPConn) ReadWithContext(ctx context.Context, buffer []byte) (int, *net.UDPAddr, error) {
	for {
		select {
		case <-ctx.Done():
			return -1, nil, ctx.Err()
		default:
		}
		if c.closed.Load() {
			return -1, nil, ErrConnectionIsClosed
		}
		n, s, err := c.connection.ReadFromUDP(buffer)
		if err != nil {
			return -1, nil, fmt.Errorf("cannot read from udp connection: %w", err)
		}
		return n, s, err
	}
}

// SetMulticastLoopback sets whether transmitted multicast packets
// should be copied and send back to the originator.
func (c *UDPConn) SetMulticastLoopback(on bool) error {
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
func (c *UDPConn) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return c.packetConn.JoinGroup(ifi, group)
}

// LeaveGroup leaves the group address group on the interface ifi
// regardless of whether the group is any-source group or source-specific group.
func (c *UDPConn) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return c.packetConn.LeaveGroup(ifi, group)
}
