package net

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
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
}

type ControlMessage struct {
	// For connection oriented packetConn the ControlMessage fields are ignored, only linux supports set control message.

	Dst     net.IP // destination address of the packet
	Src     net.IP // source address of the packet
	IfIndex int    // interface index, 0 means any interface
}

func (c *ControlMessage) String() string {
	if c == nil {
		return ""
	}
	var sb strings.Builder
	if c.Dst != nil {
		sb.WriteString(fmt.Sprintf("Dst: %s, ", c.Dst))
	}
	if c.Src != nil {
		sb.WriteString(fmt.Sprintf("Src: %s, ", c.Src))
	}
	if c.IfIndex >= 1 {
		sb.WriteString(fmt.Sprintf("IfIndex: %d, ", c.IfIndex))
	}
	return sb.String()
}

// GetIfIndex returns the interface index of the network interface. 0 means no interface index specified.
func (c *ControlMessage) GetIfIndex() int {
	if c == nil {
		return 0
	}
	return c.IfIndex
}

type packetConn interface {
	SetWriteDeadline(t time.Time) error
	WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error)
	SetMulticastInterface(ifi *net.Interface) error
	SetMulticastHopLimit(hoplim int) error
	SetMulticastLoopback(on bool) error
	JoinGroup(ifi *net.Interface, group net.Addr) error
	LeaveGroup(ifi *net.Interface, group net.Addr) error
	ReadFrom(b []byte) (n int, cm *ControlMessage, src net.Addr, err error)
	SupportsControlMessage() bool
	IsIPv6() bool
}

type packetConnIPv4 struct {
	packetConn             *ipv4.PacketConn
	supportsControlMessage bool
}

func newPacketConnIPv4(p *ipv4.PacketConn) *packetConnIPv4 {
	if err := p.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface|ipv4.FlagSrc, true); err != nil {
		return &packetConnIPv4{packetConn: p, supportsControlMessage: false}
	}
	return &packetConnIPv4{packetConn: p, supportsControlMessage: true}
}

func (p *packetConnIPv4) SupportsControlMessage() bool {
	return p.supportsControlMessage
}

func (p *packetConnIPv4) IsIPv6() bool {
	return false
}

func (p *packetConnIPv4) SetMulticastInterface(ifi *net.Interface) error {
	return p.packetConn.SetMulticastInterface(ifi)
}

func (p *packetConnIPv4) SetWriteDeadline(t time.Time) error {
	return p.packetConn.SetWriteDeadline(t)
}

func (p *packetConnIPv4) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error) {
	var c *ipv4.ControlMessage
	if cm != nil {
		c = &ipv4.ControlMessage{
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return p.packetConn.WriteTo(b, c, dst)
}

func (p *packetConnIPv4) ReadFrom(b []byte) (int, *ControlMessage, net.Addr, error) {
	n, cm, src, err := p.packetConn.ReadFrom(b)
	if err != nil {
		return -1, nil, nil, err
	}
	var controlMessage *ControlMessage
	if p.supportsControlMessage && cm != nil {
		controlMessage = &ControlMessage{
			Dst:     cm.Dst,
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return n, controlMessage, src, err
}

func (p *packetConnIPv4) SetMulticastHopLimit(hoplim int) error {
	return p.packetConn.SetMulticastTTL(hoplim)
}

func (p *packetConnIPv4) SetMulticastLoopback(on bool) error {
	return p.packetConn.SetMulticastLoopback(on)
}

func (p *packetConnIPv4) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConn.JoinGroup(ifi, group)
}

func (p *packetConnIPv4) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConn.LeaveGroup(ifi, group)
}

type packetConnIPv6 struct {
	packetConn             *ipv6.PacketConn
	supportsControlMessage bool
}

func newPacketConnIPv6(p *ipv6.PacketConn) *packetConnIPv6 {
	if err := p.SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface|ipv6.FlagSrc, true); err != nil {
		return &packetConnIPv6{packetConn: p, supportsControlMessage: false}
	}
	return &packetConnIPv6{packetConn: p, supportsControlMessage: true}
}

func (p *packetConnIPv6) SupportsControlMessage() bool {
	return p.supportsControlMessage
}

func (p *packetConnIPv6) IsIPv6() bool {
	return true
}

func (p *packetConnIPv6) SetMulticastInterface(ifi *net.Interface) error {
	return p.packetConn.SetMulticastInterface(ifi)
}

func (p *packetConnIPv6) SetWriteDeadline(t time.Time) error {
	return p.packetConn.SetWriteDeadline(t)
}

func (p *packetConnIPv6) ReadFrom(b []byte) (int, *ControlMessage, net.Addr, error) {
	n, cm, src, err := p.packetConn.ReadFrom(b)
	if err != nil {
		return -1, nil, nil, err
	}
	var controlMessage *ControlMessage
	if p.supportsControlMessage && cm != nil {
		controlMessage = &ControlMessage{
			Dst:     cm.Dst,
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return n, controlMessage, src, err
}

func (p *packetConnIPv6) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (n int, err error) {
	var c *ipv6.ControlMessage
	if cm != nil {
		c = &ipv6.ControlMessage{
			Src:     cm.Src,
			IfIndex: cm.IfIndex,
		}
	}
	return p.packetConn.WriteTo(b, c, dst)
}

func (p *packetConnIPv6) SetMulticastHopLimit(hoplim int) error {
	return p.packetConn.SetMulticastHopLimit(hoplim)
}

func (p *packetConnIPv6) SetMulticastLoopback(on bool) error {
	return p.packetConn.SetMulticastLoopback(on)
}

func (p *packetConnIPv6) JoinGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConn.JoinGroup(ifi, group)
}

func (p *packetConnIPv6) LeaveGroup(ifi *net.Interface, group net.Addr) error {
	return p.packetConn.LeaveGroup(ifi, group)
}

// IsIPv6 return's true if addr is IPV6.
func IsIPv6(addr net.IP) bool {
	if ip := addr.To16(); ip != nil && ip.To4() == nil {
		return true
	}
	return false
}

var DefaultUDPConnConfig = UDPConnConfig{
	Errors: func(error) {
		// don't log any error from fails for multicast requests
	},
}

type UDPConnConfig struct {
	Errors func(err error)
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

func newPacketConn(c *net.UDPConn) (packetConn, error) {
	laddr := c.LocalAddr()
	if laddr == nil {
		return nil, errors.New("invalid UDP connection")
	}
	addr, ok := laddr.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("invalid address type(%T), UDP address expected", laddr)
	}
	return newPacketConnWithAddr(addr, c)
}

func newPacketConnWithAddr(addr *net.UDPAddr, c *net.UDPConn) (packetConn, error) {
	var pc packetConn
	if IsIPv6(addr.IP) {
		pc = newPacketConnIPv6(ipv6.NewPacketConn(c))
	} else {
		pc = newPacketConnIPv4(ipv4.NewPacketConn(c))
	}
	return pc, nil
}

// NewUDPConn creates connection over net.UDPConn.
func NewUDPConn(network string, c *net.UDPConn, opts ...UDPOption) *UDPConn {
	cfg := DefaultUDPConnConfig
	for _, o := range opts {
		o.ApplyUDP(&cfg)
	}
	pc, err := newPacketConn(c)
	if err != nil {
		panic(err)
	}

	return &UDPConn{
		network:    network,
		connection: c,
		packetConn: pc,
		errors:     cfg.Errors,
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
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.connection.Close()
}

func toPacketSrcIP(src *net.IP, p packetConn) net.IP {
	if src != nil {
		return *src
	}
	if p.IsIPv6() {
		return net.IPv6zero
	}
	return net.IPv4zero
}

func toControlMessage(p packetConn, iface *net.Interface, src *net.IP) *ControlMessage {
	if iface != nil || src != nil {
		ifaceIdx := 0
		if iface != nil {
			ifaceIdx = iface.Index
		}
		return &ControlMessage{
			Src:     toPacketSrcIP(src, p),
			IfIndex: ifaceIdx,
		}
	}
	return nil
}

func (c *UDPConn) writeToAddr(iface *net.Interface, src *net.IP, multicastHopLimit int, raddr *net.UDPAddr, buffer []byte) error {
	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	p, err := newPacketConnWithAddr(raddr, c.connection)
	if err != nil {
		return err
	}
	if iface != nil {
		if err = p.SetMulticastInterface(iface); err != nil {
			return err
		}
	}
	if err = p.SetMulticastHopLimit(multicastHopLimit); err != nil {
		return err
	}
	cm := toControlMessage(p, iface, src)
	_, err = p.WriteTo(buffer, cm, raddr)
	return err
}

func filterAddressesByNetwork(network string, ifaceAddrs []net.Addr) []net.Addr {
	filtered := make([]net.Addr, 0, len(ifaceAddrs))
	for _, srcAddr := range ifaceAddrs {
		addrMask := srcAddr.String()
		addr := strings.Split(addrMask, "/")[0]
		if strings.Contains(addr, ":") && network == "udp4" {
			continue
		}
		if !strings.Contains(addr, ":") && network == "udp6" {
			continue
		}
		filtered = append(filtered, srcAddr)
	}
	return filtered
}

func convAddrsToIps(ifaceAddrs []net.Addr) []net.IP {
	ips := make([]net.IP, 0, len(ifaceAddrs))
	for _, addr := range ifaceAddrs {
		addrMask := addr.String()
		addr := strings.Split(addrMask, "/")[0]
		ip := net.ParseIP(addr)
		if ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

// WriteMulticast sends multicast to the remote multicast address.
// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
// Via opts you can specify the network interface, source IP address, and hop limit.
func (c *UDPConn) WriteMulticast(ctx context.Context, raddr *net.UDPAddr, buffer []byte, opts ...MulticastOption) error {
	opt := MulticastOptions{
		HopLimit: 1,
	}
	for _, o := range opts {
		o.applyMC(&opt)
	}
	return c.writeMulticast(ctx, raddr, buffer, opt)
}

func (c *UDPConn) writeMulticastWithInterface(raddr *net.UDPAddr, buffer []byte, opt MulticastOptions) error {
	if opt.Iface == nil && opt.IFaceMode == MulticastSpecificInterface {
		return errors.New("invalid interface")
	}
	if opt.Source != nil {
		return c.writeToAddr(opt.Iface, opt.Source, opt.HopLimit, raddr, buffer)
	}
	ifaceAddrs, err := opt.Iface.Addrs()
	if err != nil {
		return err
	}
	netType := "udp4"
	if IsIPv6(raddr.IP) {
		netType = "udp6"
	}
	var errors []error
	for _, ip := range convAddrsToIps(filterAddressesByNetwork(netType, ifaceAddrs)) {
		ipAddr := ip
		opt.Source = &ipAddr
		err = c.writeToAddr(opt.Iface, opt.Source, opt.HopLimit, raddr, buffer)
		if err != nil {
			errors = append(errors, err)
		}
	}
	if errors == nil {
		return nil
	}
	if len(errors) == 1 {
		return errors[0]
	}
	return fmt.Errorf("%v", errors)
}

func (c *UDPConn) writeMulticastToAllInterfaces(raddr *net.UDPAddr, buffer []byte, opt MulticastOptions) error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("cannot get interfaces for multicast connection: %w", err)
	}

	var errors []error
	for i := range ifaces {
		iface := ifaces[i]
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		if iface.Flags&net.FlagUp != net.FlagUp {
			continue
		}
		specificOpt := opt
		specificOpt.Iface = &iface
		specificOpt.IFaceMode = MulticastSpecificInterface
		err = c.writeMulticastWithInterface(raddr, buffer, specificOpt)
		if err != nil {
			if opt.InterfaceError != nil {
				opt.InterfaceError(&iface, err)
				continue
			}
			errors = append(errors, err)
		}
	}
	if errors == nil {
		return nil
	}
	if len(errors) == 1 {
		return errors[0]
	}
	return fmt.Errorf("%v", errors)
}

func (c *UDPConn) validateMulticast(ctx context.Context, raddr *net.UDPAddr, opt MulticastOptions) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if raddr == nil {
		return errors.New("cannot write multicast with context: invalid raddr")
	}
	if _, ok := c.packetConn.(*packetConnIPv4); ok && IsIPv6(raddr.IP) {
		return fmt.Errorf("cannot write multicast with context: invalid destination address(%v)", raddr.IP)
	}
	if opt.Source != nil && IsIPv6(*opt.Source) && !IsIPv6(raddr.IP) {
		return fmt.Errorf("cannot write multicast with context: invalid source address(%v) for destination(%v)", opt.Source, raddr.IP)
	}
	return nil
}

func (c *UDPConn) writeMulticast(ctx context.Context, raddr *net.UDPAddr, buffer []byte, opt MulticastOptions) error {
	err := c.validateMulticast(ctx, raddr, opt)
	if err != nil {
		return err
	}

	switch opt.IFaceMode {
	case MulticastAllInterface:
		err := c.writeMulticastToAllInterfaces(raddr, buffer, opt)
		if err != nil {
			return fmt.Errorf("cannot write multicast to all interfaces: %w", err)
		}
	case MulticastAnyInterface:
		err := c.writeToAddr(nil, opt.Source, opt.HopLimit, raddr, buffer)
		if err != nil {
			return fmt.Errorf("cannot write multicast to any: %w", err)
		}
	case MulticastSpecificInterface:
		err := c.writeMulticastWithInterface(raddr, buffer, opt)
		if err != nil {
			if opt.InterfaceError != nil {
				opt.InterfaceError(opt.Iface, err)
				return nil
			}
			return fmt.Errorf("cannot write multicast to %v: %w", opt.Iface.Name, err)
		}
	}
	return nil
}

func (c *UDPConn) writeTo(raddr *net.UDPAddr, cm *ControlMessage, buffer []byte) (int, error) {
	if !supportsOverrideRemoteAddr(c.connection) {
		// If the remote address is set, we can use it as the destination address
		// because the connection is already established.
		return c.connection.Write(buffer)
	}
	// On Linux, UDP network binds both IPv6 and IPv4 addresses to the same socket.
	// When receiving a packet from an IPv4 address, we cannot send a packet from an IPv6 address.
	// Therefore, we wrap the connection using an IPv4 packet connection (packetConn).
	if !IsIPv6(raddr.IP) && c.packetConn.IsIPv6() {
		pc := packetConnIPv4{packetConn: ipv4.NewPacketConn(c.connection)}
		return pc.WriteTo(buffer, cm, raddr)
	}
	return c.packetConn.WriteTo(buffer, cm, raddr)
}

type UDPWriteCfg struct {
	Ctx            context.Context
	RemoteAddr     *net.UDPAddr
	ControlMessage *ControlMessage
}

func (c *UDPWriteCfg) ApplyWrite(cfg *UDPWriteCfg) {
	if c.Ctx != nil {
		cfg.Ctx = c.Ctx
	}
	if c.RemoteAddr != nil {
		cfg.RemoteAddr = c.RemoteAddr
	}
	if c.ControlMessage != nil {
		cfg.ControlMessage = c.ControlMessage
	}
}

type UDPWriteOption interface {
	ApplyWrite(cfg *UDPWriteCfg)
}

type (
	UDPWriteApplyFunc func(cfg *UDPWriteCfg)
	UDPReadApplyFunc  func(cfg *UDPReadCfg)
)

type ReadWriteOptionHandler[F UDPWriteApplyFunc | UDPReadApplyFunc] struct {
	Func F
}

func (o ReadWriteOptionHandler[F]) ApplyWrite(cfg *UDPWriteCfg) {
	switch f := any(o.Func).(type) {
	case UDPWriteApplyFunc:
		f(cfg)
	default:
		panic(fmt.Errorf("invalid option handler %T for UDP Write", o.Func))
	}
}

func (o ReadWriteOptionHandler[F]) ApplyRead(cfg *UDPReadCfg) {
	switch f := any(o.Func).(type) {
	case UDPReadApplyFunc:
		f(cfg)
	default:
		panic(fmt.Errorf("invalid option handler %T for UDP Read", o.Func))
	}
}

func writeOptionFunc(f UDPWriteApplyFunc) ReadWriteOptionHandler[UDPWriteApplyFunc] {
	return ReadWriteOptionHandler[UDPWriteApplyFunc]{
		Func: f,
	}
}

type ContextOption struct {
	Ctx context.Context
}

func (o ContextOption) ApplyWrite(cfg *UDPWriteCfg) {
	cfg.Ctx = o.Ctx
}

// WithContext sets the context of operation.
func WithContext(ctx context.Context) ContextOption {
	return ContextOption{Ctx: ctx}
}

func (o ContextOption) ApplyRead(cfg *UDPReadCfg) {
	cfg.Ctx = o.Ctx
}

// WithRemoteAddr sets the remote address to packet.
func WithRemoteAddr(raddr *net.UDPAddr) UDPWriteOption {
	return writeOptionFunc(func(cfg *UDPWriteCfg) {
		cfg.RemoteAddr = raddr
	})
}

// WithControlMessage sets the control message to packet.
func WithControlMessage(cm *ControlMessage) UDPWriteOption {
	return writeOptionFunc(func(cfg *UDPWriteCfg) {
		cfg.ControlMessage = cm
	})
}

// WriteWithContext writes data with context.
func (c *UDPConn) writeWithCfg(buffer []byte, cfg UDPWriteCfg) error {
	if cfg.RemoteAddr == nil {
		return errors.New("cannot write with context: invalid raddr")
	}
	select {
	case <-cfg.Ctx.Done():
		return cfg.Ctx.Err()
	default:
	}
	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	n, err := c.writeTo(cfg.RemoteAddr, cfg.ControlMessage, buffer)
	if err != nil {
		return err
	}
	if n != len(buffer) {
		return ErrWriteInterrupted
	}

	return nil
}

// WriteWithOptions writes data with options. Via opts you can specify the remote address and control message.
func (c *UDPConn) WriteWithOptions(buffer []byte, opts ...UDPWriteOption) error {
	cfg := UDPWriteCfg{
		Ctx: context.Background(),
	}
	addr := c.RemoteAddr()
	if addr != nil {
		if remoteAddr, ok := addr.(*net.UDPAddr); ok {
			cfg.RemoteAddr = remoteAddr
		}
	}
	for _, o := range opts {
		o.ApplyWrite(&cfg)
	}
	return c.writeWithCfg(buffer, cfg)
}

// WriteWithContext writes data with context.
func (c *UDPConn) WriteWithContext(ctx context.Context, raddr *net.UDPAddr, buffer []byte) error {
	return c.WriteWithOptions(buffer, WithContext(ctx), WithRemoteAddr(raddr))
}

type UDPReadCfg struct {
	Ctx            context.Context
	RemoteAddr     **net.UDPAddr
	ControlMessage **ControlMessage
}

func (c *UDPReadCfg) ApplyRead(cfg *UDPReadCfg) {
	if c.Ctx != nil {
		cfg.Ctx = c.Ctx
	}
	if c.RemoteAddr != nil {
		cfg.RemoteAddr = c.RemoteAddr
	}
	if c.ControlMessage != nil {
		cfg.ControlMessage = c.ControlMessage
	}
}

type UDPReadOption interface {
	ApplyRead(cfg *UDPReadCfg)
}

func readOptionFunc(f UDPReadApplyFunc) UDPReadOption {
	return ReadWriteOptionHandler[UDPReadApplyFunc]{
		Func: f,
	}
}

// WithGetRemoteAddr fills the remote address when reading succeeds.
func WithGetRemoteAddr(raddr **net.UDPAddr) UDPReadOption {
	return readOptionFunc(func(cfg *UDPReadCfg) {
		cfg.RemoteAddr = raddr
	})
}

// WithGetControlMessage fills the control message when reading succeeds.
func WithGetControlMessage(cm **ControlMessage) UDPReadOption {
	return readOptionFunc(func(cfg *UDPReadCfg) {
		cfg.ControlMessage = cm
	})
}

func (c *UDPConn) readWithCfg(buffer []byte, cfg UDPReadCfg) (int, error) {
	select {
	case <-cfg.Ctx.Done():
		return -1, cfg.Ctx.Err()
	default:
	}
	if c.closed.Load() {
		return -1, ErrConnectionIsClosed
	}
	n, cm, srcAddr, err := c.packetConn.ReadFrom(buffer)
	if err != nil {
		return -1, fmt.Errorf("cannot read from udp connection: %w", err)
	}
	if udpAdrr, ok := srcAddr.(*net.UDPAddr); ok {
		if cfg.RemoteAddr != nil {
			*cfg.RemoteAddr = udpAdrr
		}
		if cfg.ControlMessage != nil {
			*cfg.ControlMessage = cm
		}
		return n, nil
	}
	return -1, fmt.Errorf("cannot read from udp connection: invalid srcAddr type %T", srcAddr)
}

// ReadWithOptions reads packet with options. Via opts you can get also the remote address and control message.
func (c *UDPConn) ReadWithOptions(buffer []byte, opts ...UDPReadOption) (int, error) {
	cfg := UDPReadCfg{
		Ctx: context.Background(),
	}
	for _, o := range opts {
		o.ApplyRead(&cfg)
	}
	return c.readWithCfg(buffer, cfg)
}

// ReadWithContext reads packet with context.
func (c *UDPConn) ReadWithContext(ctx context.Context, buffer []byte) (int, *net.UDPAddr, error) {
	var remoteAddr *net.UDPAddr
	n, err := c.ReadWithOptions(buffer, WithContext(ctx), WithGetRemoteAddr(&remoteAddr))
	if err != nil {
		return -1, nil, err
	}
	return n, remoteAddr, err
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

// NetConn returns the underlying connection that is wrapped by c. The Conn returned is shared by all invocations of NetConn, so do not modify it.
func (c *UDPConn) NetConn() net.Conn {
	return c.connection
}
