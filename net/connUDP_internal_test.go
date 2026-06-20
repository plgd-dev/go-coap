package net

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	udpNetwork  = "udp"
	udp4Network = "udp4"
	udp6Network = "udp6"
)

type multicastWriteArgs struct {
	ctx     context.Context
	udpAddr *net.UDPAddr
	buffer  []byte
	opts    []MulticastOption
}

type multicastWriteTestCase struct {
	name    string
	args    multicastWriteArgs
	wantErr bool
	skip    func() bool
}

func TestUDPConnWriteWithContext(t *testing.T) {
	iface := getInterfaceIndex(t)
	ifaceIpv4 := getIfaceAddr(t, iface, true)
	peerAddr := ifaceIpv4.String() + ":2154"
	b, err := net.ResolveUDPAddr(udpNetwork, peerAddr)
	require.NoError(t, err)

	ctxCanceled, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()

	type args struct {
		ctx           context.Context
		listenNetwork string
		udpAddr       *net.UDPAddr
		buffer        []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid - udp4 network",
			args: args{
				ctx:           context.Background(),
				listenNetwork: udp4Network,
				udpAddr:       b,
				buffer:        []byte("hello world"),
			},
		},
		{
			name: "cancelled",
			args: args{
				ctx:           ctxCanceled,
				listenNetwork: udp4Network,
				buffer:        []byte("hello world"),
			},
			wantErr: true,
		},
		{
			name: "send to v4 from v6 socket",
			args: args{
				ctx:           context.Background(),
				listenNetwork: udpNetwork,
				udpAddr:       b,
				buffer:        []byte("hello world"),
			},
			wantErr: false,
		},
	}
	if runtime.GOOS == "linux" {
		tests = append(tests, struct {
			name    string
			args    args
			wantErr bool
		}{
			name: "valid - udp network",
			args: args{
				ctx:           context.Background(),
				listenNetwork: udpNetwork,
				udpAddr:       b,
				buffer:        []byte("hello world"),
			},
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l2, err := net.ListenUDP(udpNetwork, b)
	require.NoError(t, err)
	c2 := NewUDPConn(udpNetwork, l2, WithErrors(func(err error) { t.Log(err) }))
	defer func() {
		errC := c2.Close()
		require.NoError(t, errC)
	}()

	go func() {
		b := make([]byte, 1024)
		_, _, errR := c2.ReadWithContext(ctx, b)
		if errR != nil {
			return
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := net.ResolveUDPAddr(tt.args.listenNetwork, ":")
			require.NoError(t, err)
			l1, err := net.ListenUDP(tt.args.listenNetwork, a)
			require.NoError(t, err)
			c1 := NewUDPConn(tt.args.listenNetwork, l1, WithErrors(func(err error) { t.Log(err) }))
			defer func() {
				errC := c1.Close()
				require.NoError(t, errC)
			}()

			err = c1.WriteWithOptions(tt.args.buffer, WithContext(ctx), WithRemoteAddr(tt.args.udpAddr), WithControlMessage(&ControlMessage{
				IfIndex: iface.Index,
			}))

			c1.LocalAddr()
			c1.RemoteAddr()

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUDPConnwriteMulticastWithContext(t *testing.T) {
	peerAddr := "224.0.1.187:9999"
	b, err := net.ResolveUDPAddr(udp4Network, peerAddr)
	require.NoError(t, err)

	ctxCanceled, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	payload := []byte("hello world")
	const goosDarwin = "darwin"

	iface := getFirstUpMulticastInterface(t)
	require.NotEmpty(t, iface)

	tests := []multicastWriteTestCase{
		{
			name: "valid all interfaces",
			args: multicastWriteArgs{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithAllMulticastInterface()},
			},
			skip: func() bool {
				return runtime.GOOS == goosDarwin
			},
		},
		{
			name: "valid any interface",
			args: multicastWriteArgs{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithAnyMulticastInterface()},
			},
			skip: func() bool {
				return runtime.GOOS == goosDarwin
			},
		},
		{
			name: "valid first interface",
			args: multicastWriteArgs{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithMulticastInterface(iface)},
			},
		},
		{
			name: "cancelled",
			args: multicastWriteArgs{
				ctx:     ctxCanceled,
				udpAddr: b,
				buffer:  payload,
			},
			wantErr: true,
		},
	}

	listenAddr := ":" + strconv.Itoa(b.Port)
	c, err := net.ResolveUDPAddr(udp4Network, listenAddr)
	require.NoError(t, err)
	l2, err := net.ListenUDP(udp4Network, c)
	require.NoError(t, err)
	c2 := NewUDPConn(udpNetwork, l2, WithErrors(func(err error) { t.Log(err) }))
	defer func() {
		errC := c2.Close()
		require.NoError(t, errC)
	}()
	joinMulticastGroupOnAllInterfaces(t, c2, b)

	err = c2.SetMulticastLoopback(true)
	require.NoError(t, err)

	a, err := net.ResolveUDPAddr(udp4Network, "")
	require.NoError(t, err)
	l1, err := net.ListenUDP(udp4Network, a)
	require.NoError(t, err)
	c1 := NewUDPConn(udpNetwork, l1, WithErrors(func(err error) { t.Log(err) }))
	defer func() {
		errC := c1.Close()
		require.NoError(t, errC)
	}()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		b := make([]byte, 1024)
		n, _, errR := c2.ReadWithContext(ctx, b)
		assert.NoError(t, errR)
		if n > 0 {
			b = b[:n]
			assert.Equal(t, payload, b)
		}
		wg.Done()
	}()
	defer wg.Wait()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runWriteMulticastCase(t, c1, tt)
		})
	}
}

func getFirstUpMulticastInterface(t *testing.T) net.Interface {
	t.Helper()
	ifs, err := net.Interfaces()
	require.NoError(t, err)
	for _, i := range ifs {
		if i.Flags&net.FlagMulticast == net.FlagMulticast && i.Flags&net.FlagUp == net.FlagUp {
			return i
		}
	}
	require.FailNow(t, "No suitable multicast interface found")
	return net.Interface{}
}

func joinMulticastGroupOnAllInterfaces(t *testing.T, c *UDPConn, group *net.UDPAddr) {
	t.Helper()
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		ifa := iface
		err = c.JoinGroup(&ifa, group)
		if err != nil {
			t.Logf("fmt cannot join group %v: %v", ifa.Name, err)
		}
	}
}

func runWriteMulticastCase(t *testing.T, c *UDPConn, tt multicastWriteTestCase) {
	t.Helper()
	if tt.skip != nil && tt.skip() {
		t.Log("skipped")
		return
	}
	err := c.WriteMulticast(tt.args.ctx, tt.args.udpAddr, tt.args.buffer, tt.args.opts...)
	c.LocalAddr()
	c.RemoteAddr()

	if tt.wantErr {
		require.Error(t, err)
		return
	}
	require.NoError(t, err)
}

func TestControlMessageString(t *testing.T) {
	tests := []struct {
		name string
		c    *ControlMessage
		want string
	}{
		{
			name: "nil",
			c:    nil,
			want: "",
		},
		{
			name: "dst",
			c: &ControlMessage{
				Dst: net.IPv4(192, 168, 1, 1),
			},
			want: "Dst: 192.168.1.1, ",
		},
		{
			name: "src",
			c: &ControlMessage{
				Src: net.IPv4(192, 168, 1, 2),
			},
			want: "Src: 192.168.1.2, ",
		},
		{
			name: "ifIndex",
			c: &ControlMessage{
				IfIndex: 1,
			},
			want: "IfIndex: 1, ",
		},
		{
			name: "all",
			c: &ControlMessage{
				Dst:     net.IPv4(192, 168, 1, 1),
				Src:     net.IPv4(192, 168, 1, 2),
				IfIndex: 1,
			},
			want: "Dst: 192.168.1.1, Src: 192.168.1.2, IfIndex: 1, ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.c.String())
		})
	}
}

func getIfaceAddr(t *testing.T, iface net.Interface, ipv4 bool) net.IP {
	addrs, err := iface.Addrs()
	require.NoError(t, err)
	require.NotEmpty(t, addrs)
	var selectedIP net.IP
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			t.Logf("Error parsing CIDR: %v", err)
			continue
		}
		if ip.IsPrivate() && (!ipv4 || ip.To4() != nil) {
			selectedIP = ip
			break
		}
	}
	require.NotEmpty(t, selectedIP, "No suitable IP address found")
	return selectedIP
}

func isActiveMulticastInterface(iface net.Interface) bool {
	if iface.Name == "anpi0" || iface.Name == "anpi1" { // special debugging interfaces on macOS
		return false
	}
	if iface.Flags&net.FlagUp == net.FlagUp && iface.Flags&net.FlagMulticast == net.FlagMulticast && iface.Flags&net.FlagLoopback != net.FlagLoopback {
		addrs, err := iface.Addrs()
		return err == nil && len(addrs) > 0
	}
	return false
}

func getInterfaceIndex(t *testing.T) net.Interface {
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, i := range ifaces {
		t.Logf("interface name:%v, flags: %v", i.Name, i.Flags)
		if isActiveMulticastInterface(i) {
			return i
		}
	}
	require.Fail(t, "No suitable interface found")
	return net.Interface{}
}

type writeToAddrArgs struct {
	iface             *net.Interface
	src               net.IP
	multicastHopLimit int
	raddr             *net.UDPAddr
	buffer            []byte
}

type writeToAddrTestCase struct {
	name    string
	args    writeToAddrArgs
	wantErr bool
	skip    func() bool
}

func TestUDPConnWriteToAddr(t *testing.T) {
	iface := getInterfaceIndex(t)
	tests := []writeToAddrTestCase{
		{
			name: "IPv4",
			args: writeToAddrArgs{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "IPv6",
			args: writeToAddrArgs{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, false), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "closed",
			args: writeToAddrArgs{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
			wantErr: true,
		},
		{
			name: "with interface",
			args: writeToAddrArgs{
				iface:  &iface,
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "with source",
			args: writeToAddrArgs{
				src:    net.IP{127, 0, 0, 1},
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "with multicast hop limit",
			args: writeToAddrArgs{
				multicastHopLimit: 5,
				raddr:             &net.UDPAddr{IP: net.IPv4(224, 0, 0, 1), Port: 1234},
				buffer:            []byte("hello world"),
			},
			skip: func() bool {
				return runtime.GOOS == "darwin"
			},
		},
		{
			name: "with interface and source",
			args: writeToAddrArgs{
				iface:  &iface,
				src:    getIfaceAddr(t, iface, true),
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runUDPConnWriteToAddrCase(t, iface, tt)
		})
	}
}

func runUDPConnWriteToAddrCase(t *testing.T, iface net.Interface, tt writeToAddrTestCase) {
	t.Helper()
	if tt.skip != nil && tt.skip() {
		t.Log("skipped")
		return
	}
	network := udp4Network
	ip := getIfaceAddr(t, iface, true)
	// The listen socket family must match the destination address family,
	// because writeToAddr builds the packetConn from raddr. Derive it from
	// the source address when set, otherwise from raddr itself.
	if IsIPv6(tt.args.src) || (tt.args.src == nil && IsIPv6(tt.args.raddr.IP)) {
		network = udp6Network
		ip = getIfaceAddr(t, iface, false)
	}
	p, err := net.ListenUDP(network, &net.UDPAddr{IP: ip, Port: 1235})
	require.NoError(t, err)
	defer func() {
		errC := p.Close()
		require.NoError(t, errC)
	}()
	c := &UDPConn{
		connection: p,
	}
	if tt.wantErr {
		c.closed.Store(true)
	}
	err = c.writeToAddr(tt.args.iface, &tt.args.src, tt.args.multicastHopLimit, tt.args.raddr, tt.args.buffer)
	if tt.wantErr {
		require.Error(t, err)
		return
	}
	require.NoError(t, err)
}

// TestUDPConnDualStackWriteDelivers verifies that a server response from a dual-stack
// ("udp" / [::]) listener to an IPv4 client is accepted AND actually delivered, on every
// platform.
//
// This is the combination that needs explicit coverage:
//   - Windows: the underlying WriteMsgUDP reports n=0 (must still count as success via the
//     writeWithCfg guard), and
//   - macOS: ipv4.PacketConn.WriteTo cannot send to an IPv4 destination from a [::] socket
//     ("sendmsg: invalid argument"), which is why the portable WriteMsgUDP path is kept.
//
// The existing TestUDPConnWriteWithContext/"send to v4 from v6 socket" only asserts the
// absence of an error; this test additionally asserts the datagram is received.
func TestUDPConnDualStackWriteDelivers(t *testing.T) {
	// Dual-stack listener (IPv6 socket that also accepts IPv4 via v4-mapped addresses).
	server, err := NewListenUDP(udpNetwork, "[::]:0")
	if err != nil {
		t.Skipf("cannot create dual-stack listener: %v", err)
	}
	defer func() {
		errC := server.Close()
		require.NoError(t, errC)
	}()
	require.True(t, supportsOverrideRemoteAddr(server.connection))
	// Confirm we really exercise the v4-from-v6 path: the listener socket is IPv6.
	require.True(t, server.packetConn.IsIPv6(), "listener should be a dual-stack IPv6 socket")

	serverAddr, ok := server.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	// IPv4 client talking to the dual-stack server over loopback.
	client, err := net.DialUDP(udp4Network, nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: serverAddr.Port})
	if err != nil {
		t.Skipf("cannot dial IPv4 to dual-stack listener: %v", err)
	}
	defer func() {
		errC := client.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	// The client pings first so the server learns the remote address in whatever form the
	// OS reports it (mirrors a real server reply flow).
	_, err = client.Write([]byte("ping"))
	require.NoError(t, err)

	buf := make([]byte, 64)
	var raddr *net.UDPAddr
	_, err = server.ReadWithOptions(buf, WithContext(ctx), WithGetRemoteAddr(&raddr))
	require.NoError(t, err)
	require.NotNil(t, raddr)

	payload := make([]byte, 1500)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	// The server reply path must succeed (no bogus ErrWriteInterrupted on Windows) and the
	// datagram must actually arrive (no silent drop / family mismatch on macOS).
	err = server.WriteWithContext(ctx, raddr, payload)
	require.NoError(t, err)

	errR := client.SetReadDeadline(time.Now().Add(time.Second * 2))
	require.NoError(t, errR)
	got := make([]byte, len(payload)+16)
	n, errR := client.Read(got)
	require.NoError(t, errR)
	require.Equal(t, payload, got[:n])
}

// TestUDPConnUnconnectedWriteReportsFullWrite verifies that writing a datagram from an
// unconnected server listener (supportsOverrideRemoteAddr == true) reports the full number
// of written bytes and does not return ErrWriteInterrupted.
//
// Regression test for the Windows bug where (*net.UDPConn).WriteMsgUDP reports n=0 even
// though the datagram is delivered, which made writeWithCfg treat every server response as
// a partial write and broke blockwise transfers. The fix hardens writeWithCfg to treat a
// zero-length report as success (a UDP datagram is delivered atomically), while keeping the
// portable, dual-stack capable WriteMsgUDP write path. Before the fix this test fails on
// Windows; after the fix it passes on all platforms.
func TestUDPConnUnconnectedWriteReportsFullWrite(t *testing.T) {
	type netCfg struct {
		network  string
		listenIP string
		dialIP   string
	}
	netCfgs := []netCfg{
		{network: udp4Network, listenIP: "127.0.0.1", dialIP: "127.0.0.1"},
		{network: udp6Network, listenIP: "::1", dialIP: "::1"},
	}
	sizes := []int{1, 256, 512, 1024, 2000}

	for _, nc := range netCfgs {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s/size=%d", nc.network, size), func(t *testing.T) {
				server, err := NewListenUDP(nc.network, net.JoinHostPort(nc.listenIP, "0"))
				if err != nil {
					t.Skipf("cannot listen on %s: %v", nc.network, err)
				}
				defer func() {
					errC := server.Close()
					require.NoError(t, errC)
				}()
				// Sanity: the server listener must be unconnected so that the
				// supportsOverrideRemoteAddr write path (the buggy one) is exercised.
				require.True(t, supportsOverrideRemoteAddr(server.connection))

				serverAddr, ok := server.LocalAddr().(*net.UDPAddr)
				require.True(t, ok)
				// Use the loopback dial IP instead of the (possibly unspecified) listen IP.
				dialAddr := &net.UDPAddr{IP: net.ParseIP(nc.dialIP), Port: serverAddr.Port}

				client, err := net.DialUDP(nc.network, nil, dialAddr)
				require.NoError(t, err)
				defer func() {
					errC := client.Close()
					require.NoError(t, errC)
				}()

				clientAddr, ok := client.LocalAddr().(*net.UDPAddr)
				require.True(t, ok)

				payload := make([]byte, size)
				for i := range payload {
					payload[i] = byte(i % 251)
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()

				// This is the assertion that fails on Windows before the fix: writeTo
				// returns n=0 -> writeWithCfg returns ErrWriteInterrupted.
				err = server.WriteWithContext(ctx, clientAddr, payload)
				require.NoError(t, err)

				errR := client.SetReadDeadline(time.Now().Add(time.Second * 2))
				require.NoError(t, errR)
				got := make([]byte, size+16)
				n, errR := client.Read(got)
				require.NoError(t, errR)
				require.Equal(t, payload, got[:n])
			})
		}
	}
}

func TestUDPConnConnectedWriteDelivers(t *testing.T) {
	listener, err := net.ListenUDP(udp4Network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)
	defer func() {
		errC := listener.Close()
		require.NoError(t, errC)
	}()

	serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	client, err := net.DialUDP(udp4Network, nil, serverAddr)
	require.NoError(t, err)
	defer func() {
		errC := client.Close()
		require.NoError(t, errC)
	}()

	require.NotNil(t, client.RemoteAddr())
	conn := NewUDPConn(udp4Network, client, WithErrors(func(err error) { t.Log(err) }))

	payload := []byte("connected write")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	err = conn.WriteWithContext(ctx, serverAddr, payload)
	require.NoError(t, err)

	errR := listener.SetReadDeadline(time.Now().Add(time.Second * 2))
	require.NoError(t, errR)
	got := make([]byte, len(payload)+16)
	n, errR := listener.Read(got)
	require.NoError(t, errR)
	require.Equal(t, payload, got[:n])
}

func TestUDPConnWriteToBranches(t *testing.T) {
	t.Run("connected connection uses Write", func(t *testing.T) {
		listener, err := net.ListenUDP(udp4Network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		require.NoError(t, err)
		defer func() {
			errC := listener.Close()
			require.NoError(t, errC)
		}()

		serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
		require.True(t, ok)

		client, err := net.DialUDP(udp4Network, nil, serverAddr)
		require.NoError(t, err)
		defer func() {
			errC := client.Close()
			require.NoError(t, errC)
		}()

		conn := &UDPConn{connection: client}
		payload := []byte("connected writeTo")
		n, err := conn.writeTo(serverAddr, nil, payload)
		require.NoError(t, err)
		require.Equal(t, len(payload), n)

		errR := listener.SetReadDeadline(time.Now().Add(time.Second * 2))
		require.NoError(t, errR)
		got := make([]byte, len(payload)+16)
		n, errR = listener.Read(got)
		require.NoError(t, errR)
		require.Equal(t, payload, got[:n])
	})

	t.Run("override remote addr with ipv4 control message", func(t *testing.T) {
		listener, err := net.ListenUDP(udp4Network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		require.NoError(t, err)
		defer func() {
			errC := listener.Close()
			require.NoError(t, errC)
		}()

		serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
		require.True(t, ok)

		client, err := net.DialUDP(udp4Network, nil, serverAddr)
		require.NoError(t, err)
		defer func() {
			errC := client.Close()
			require.NoError(t, errC)
		}()

		conn := &UDPConn{connection: listener}
		payload := []byte("ipv4 control")
		n, err := conn.writeTo(client.LocalAddr().(*net.UDPAddr), &ControlMessage{Src: net.IPv4(127, 0, 0, 1)}, payload)
		require.NoError(t, err)
		require.Equal(t, len(payload), n)

		errR := client.SetReadDeadline(time.Now().Add(time.Second * 2))
		require.NoError(t, errR)
		got := make([]byte, len(payload)+16)
		n, errR = client.Read(got)
		require.NoError(t, errR)
		require.Equal(t, payload, got[:n])
	})

	t.Run("override remote addr with ipv6 control message", func(t *testing.T) {
		listener, err := net.ListenUDP(udp6Network, &net.UDPAddr{IP: net.ParseIP("::1"), Port: 0})
		if err != nil {
			t.Skipf("cannot listen on udp6: %v", err)
		}
		defer func() {
			errC := listener.Close()
			require.NoError(t, errC)
		}()

		serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
		require.True(t, ok)

		client, err := net.DialUDP(udp6Network, nil, serverAddr)
		if err != nil {
			t.Skipf("cannot dial udp6: %v", err)
		}
		defer func() {
			errC := client.Close()
			require.NoError(t, errC)
		}()

		conn := &UDPConn{connection: listener}
		payload := []byte("ipv6 control")
		n, err := conn.writeTo(client.LocalAddr().(*net.UDPAddr), &ControlMessage{Src: net.ParseIP("::1")}, payload)
		require.NoError(t, err)
		require.Equal(t, len(payload), n)

		errR := client.SetReadDeadline(time.Now().Add(time.Second * 2))
		require.NoError(t, errR)
		got := make([]byte, len(payload)+16)
		n, errR = client.Read(got)
		require.NoError(t, errR)
		require.Equal(t, payload, got[:n])
	})
}

func TestUDPConnWriteWithCfgBranches(t *testing.T) {
	conn := &UDPConn{}

	t.Run("missing remote addr", func(t *testing.T) {
		err := conn.writeWithCfg([]byte("x"), UDPWriteCfg{Ctx: context.Background()})
		require.Error(t, err)
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := conn.writeWithCfg([]byte("x"), UDPWriteCfg{Ctx: ctx, RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}})
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("closed connection", func(t *testing.T) {
		conn.closed.Store(true)
		defer conn.closed.Store(false)
		err := conn.writeWithCfg([]byte("x"), UDPWriteCfg{Ctx: context.Background(), RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}})
		require.ErrorIs(t, err, ErrConnectionIsClosed)
	})

	t.Run("writeTo error", func(t *testing.T) {
		original := udpConnWriteTo
		udpConnWriteTo = func(*UDPConn, *net.UDPAddr, *ControlMessage, []byte) (int, error) {
			return 0, fmt.Errorf("boom")
		}
		defer func() {
			udpConnWriteTo = original
		}()
		err := conn.writeWithCfg([]byte("x"), UDPWriteCfg{Ctx: context.Background(), RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}})
		require.ErrorContains(t, err, "boom")
	})

	t.Run("short write", func(t *testing.T) {
		original := udpConnWriteTo
		udpConnWriteTo = func(*UDPConn, *net.UDPAddr, *ControlMessage, []byte) (int, error) {
			return 1, nil
		}
		defer func() {
			udpConnWriteTo = original
		}()
		err := conn.writeWithCfg([]byte("xyz"), UDPWriteCfg{Ctx: context.Background(), RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}})
		require.ErrorIs(t, err, ErrWriteInterrupted)
	})
}

func TestPacketConnReadFrom(t *testing.T) {
	readUDP4Conn, err := net.ListenUDP(udp4Network, &net.UDPAddr{Port: 1234})
	require.NoError(t, err)
	defer func() {
		errC := readUDP4Conn.Close()
		require.NoError(t, errC)
	}()

	require.Nil(t, readUDP4Conn.RemoteAddr())

	writeUDP4Conn, err := net.DialUDP(udp4Network, nil, readUDP4Conn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer func() {
		errC := writeUDP4Conn.Close()
		require.NoError(t, errC)
	}()

	require.NotNil(t, writeUDP4Conn.RemoteAddr())

	readUDP6Conn, err := net.ListenUDP(udp6Network, &net.UDPAddr{Port: 1235})
	require.NoError(t, err)
	defer func() {
		errC := readUDP6Conn.Close()
		require.NoError(t, errC)
	}()
	writeUDP6Conn, err := net.DialUDP(udp6Network, nil, readUDP6Conn.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer func() {
		errC := writeUDP6Conn.Close()
		require.NoError(t, errC)
	}()

	type fields struct {
		packetConn *net.UDPConn
	}
	type args struct {
		b []byte
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantN   int
		wantErr bool
	}{
		{
			name: "valid UDP4",
			fields: fields{
				packetConn: readUDP4Conn,
			},
			args: args{
				b: []byte("hello world"),
			},
			wantN:   11,
			wantErr: false,
		},
		{
			name: "valid UDP6",
			fields: fields{
				packetConn: readUDP6Conn,
			},
			args: args{
				b: []byte("hello world"),
			},
			wantN:   11,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := newPacketConn(tt.fields.packetConn)
			require.NoError(t, err)
			if !tt.wantErr && tt.fields.packetConn == readUDP4Conn {
				n, errW := writeUDP4Conn.Write(tt.args.b)
				require.NoError(t, errW)
				require.Equal(t, len(tt.args.b), n)
			}
			if !tt.wantErr && tt.fields.packetConn == readUDP6Conn {
				n, errW := writeUDP6Conn.Write(tt.args.b)
				require.NoError(t, errW)
				require.Equal(t, len(tt.args.b), n)
			}
			gotN, gotCm, gotSrc, err := p.ReadFrom(tt.args.b)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if p.SupportsControlMessage() {
				require.NotNil(t, gotCm)
			} else {
				require.Nil(t, gotCm)
			}
			require.NotNil(t, gotSrc)
			require.Equal(t, tt.wantN, gotN)
		})
	}
}
