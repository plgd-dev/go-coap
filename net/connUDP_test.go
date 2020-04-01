package net

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnUDP_WriteWithContext(t *testing.T) {
	peerAddr := "127.0.0.1:2154"
	b, err := net.ResolveUDPAddr("udp", peerAddr)
	assert.NoError(t, err)

	ctxCanceled, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()

	type args struct {
		ctx    context.Context
		udpCtx *ConnUDPContext
		buffer []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid",
			args: args{
				ctx:    context.Background(),
				udpCtx: NewConnUDPContext(b),
				buffer: []byte("hello world"),
			},
		},
		{
			name: "cancelled",
			args: args{
				ctx:    ctxCanceled,
				buffer: []byte("hello world"),
			},
			wantErr: true,
		},
	}

	a, err := net.ResolveUDPAddr("udp", "127.0.0.1:")
	assert.NoError(t, err)
	l1, err := net.ListenUDP("udp", a)
	assert.NoError(t, err)
	c1 := NewConnUDP(l1, time.Millisecond*100, 0, func(err error) { t.Log(err) })
	defer c1.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l2, err := net.ListenUDP("udp", b)
	assert.NoError(t, err)
	c2 := NewConnUDP(l2, time.Millisecond*100, 0, func(err error) { t.Log(err) })
	defer c2.Close()

	go func() {
		b := make([]byte, 1024)
		_, _, err := c2.ReadWithContext(ctx, b)
		if err != nil {
			return
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err = c1.WriteWithContext(tt.args.ctx, tt.args.udpCtx, tt.args.buffer)

			c1.LocalAddr()
			c1.RemoteAddr()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConnUDP_writeMulticastWithContext(t *testing.T) {
	peerAddr := "224.0.1.187:5683"
	b, err := net.ResolveUDPAddr("udp4", peerAddr)
	assert.NoError(t, err)

	ctxCanceled, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	payload := []byte("hello world")

	type args struct {
		ctx    context.Context
		udpCtx *ConnUDPContext
		buffer []byte
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid",
			args: args{
				ctx:    context.Background(),
				udpCtx: NewConnUDPContext(b),
				buffer: payload,
			},
		},
		{
			name: "cancelled",
			args: args{
				ctx:    ctxCanceled,
				udpCtx: NewConnUDPContext(b),
				buffer: payload,
			},
			wantErr: true,
		},
	}

	listenAddr := ":" + strconv.Itoa(b.Port)
	c, err := net.ResolveUDPAddr("udp4", listenAddr)
	assert.NoError(t, err)
	l2, err := net.ListenUDP("udp4", c)
	assert.NoError(t, err)
	c2 := NewConnUDP(l2, time.Millisecond*100, 2, func(err error) { t.Log(err) })
	defer c2.Close()
	ifaces, err := net.Interfaces()
	require.NoError(t, err)
	for _, iface := range ifaces {
		ifa := iface
		err = c2.JoinGroup(&ifa, b)
		if err != nil {
			t.Logf("fmt cannot join group %v: %v", ifa.Name, err)
		}
	}

	assert.NoError(t, err)
	err = c2.SetMulticastLoopback(true)
	assert.NoError(t, err)

	a, err := net.ResolveUDPAddr("udp4", "")
	assert.NoError(t, err)
	l1, err := net.ListenUDP("udp4", a)
	assert.NoError(t, err)
	c1 := NewConnUDP(l1, time.Millisecond*100, 2, func(err error) { t.Log(err) })
	defer c1.Close()
	assert.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		b := make([]byte, 1024)
		n, _, err := c2.ReadWithContext(ctx, b)
		assert.NoError(t, err)
		if n > 0 {
			b = b[:n]
			assert.Equal(t, payload, b)
		}
		wg.Done()
	}()
	defer wg.Wait()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err = c1.WriteWithContext(tt.args.ctx, tt.args.udpCtx, tt.args.buffer)

			c1.LocalAddr()
			c1.RemoteAddr()

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
