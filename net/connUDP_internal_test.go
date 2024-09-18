package net

import (
	"context"
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

	ifs, err := net.Interfaces()
	require.NoError(t, err)
	var iface net.Interface
	for _, i := range ifs {
		if i.Flags&net.FlagMulticast == net.FlagMulticast && i.Flags&net.FlagUp == net.FlagUp {
			iface = i
			break
		}
	}
	require.NotEmpty(t, iface)

	type args struct {
		ctx     context.Context
		udpAddr *net.UDPAddr
		buffer  []byte
		opts    []MulticastOption
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid all interfaces",
			args: args{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithAllMulticastInterface()},
			},
		},
		{
			name: "valid any interface",
			args: args{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithAnyMulticastInterface()},
			},
		},
		{
			name: "valid first interface",
			args: args{
				ctx:     context.Background(),
				udpAddr: b,
				buffer:  payload,
				opts:    []MulticastOption{WithMulticastInterface(iface)},
			},
		},
		{
			name: "cancelled",
			args: args{
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
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		ifa := iface
		err = c2.JoinGroup(&ifa, b)
		if err != nil {
			t.Logf("fmt cannot join group %v: %v", ifa.Name, err)
		}
	}

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
			err = c1.WriteMulticast(tt.args.ctx, tt.args.udpAddr, tt.args.buffer, tt.args.opts...)
			c1.LocalAddr()
			c1.RemoteAddr()

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
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

func TestUDPConnWriteToAddr(t *testing.T) {
	iface := getInterfaceIndex(t)
	type args struct {
		iface             *net.Interface
		src               net.IP
		multicastHopLimit int
		raddr             *net.UDPAddr
		buffer            []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "IPv4",
			args: args{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "IPv6",
			args: args{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, false), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "closed",
			args: args{
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
			wantErr: true,
		},
		{
			name: "with interface",
			args: args{
				iface:  &iface,
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "with source",
			args: args{
				src:    net.IP{127, 0, 0, 1},
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
		{
			name: "with multicast hop limit",
			args: args{
				multicastHopLimit: 5,
				raddr:             &net.UDPAddr{IP: net.IPv4(224, 0, 0, 1), Port: 1234},
				buffer:            []byte("hello world"),
			},
		},
		{
			name: "with interface and source",
			args: args{
				iface:  &iface,
				src:    getIfaceAddr(t, iface, true),
				raddr:  &net.UDPAddr{IP: getIfaceAddr(t, iface, true), Port: 1234},
				buffer: []byte("hello world"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			network := udp4Network
			ip := getIfaceAddr(t, iface, true)
			if IsIPv6(tt.args.src) {
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
		})
	}
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
