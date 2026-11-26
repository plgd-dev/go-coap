package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

type expirationRecordingCache struct {
	sweeps atomic.Int32
}

func (*expirationRecordingCache) Load(string, *pool.Message) (bool, error) { return false, nil }
func (*expirationRecordingCache) Store(string, *pool.Message) error        { return nil }
func (c *expirationRecordingCache) CheckExpirations(time.Time)             { c.sweeps.Add(1) }

type expirationRecordingMonitor struct {
	checks  atomic.Int32
	notices atomic.Int32
	close   atomic.Bool
}

func (m *expirationRecordingMonitor) Notify() { m.notices.Add(1) }
func (m *expirationRecordingMonitor) CheckInactivity(_ time.Time, cc *client.Conn) {
	m.checks.Add(1)
	if m.close.Load() {
		_ = cc.Close()
	}
}

func TestGetConnReplacesInactiveConnection(t *testing.T) {
	server := New()
	defer server.Stop()
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local := listener.LocalAddr().(*net.UDPAddr)
	monitor := &expirationRecordingMonitor{}
	server.cfg.CreateInactivityMonitor = func() client.InactivityMonitor { return monitor }
	first, created, err := server.getConn(listener, remote, local, true)
	require.NoError(t, err)
	require.True(t, created)
	monitor.close.Store(true)
	next, created, err := server.getConn(listener, remote, local, true)
	require.NoError(t, err)
	require.True(t, created)
	require.NotSame(t, first, next)
	require.ErrorIs(t, first.Context().Err(), context.Canceled)
	require.Zero(t, monitor.notices.Load(), "closed connections must not be refreshed")
	require.NoError(t, next.Context().Err())
	getClose(next)()
}

func TestGetConnOnlyChecksInactivity(t *testing.T) {
	server := New()
	defer server.Stop()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	session := NewSession(context.Background(), context.Background(), nil, remote, 1024, 1024, false)
	defer session.shutdown()
	defer session.Close()
	cache := &expirationRecordingCache{}
	monitor := &expirationRecordingMonitor{}
	cfg := client.DefaultConfig
	cfg.BlockwiseEnable = false
	cc := client.NewConnWithOpts(session, &cfg,
		client.WithResponseMessageCache(cache), client.WithInactivityMonitor(monitor))
	server.conns[getConnKey(remote, local)] = cc

	for range 100 {
		got, created, err := server.getConn(nil, remote, local, true)
		require.NoError(t, err)
		require.Same(t, cc, got)
		require.False(t, created)
	}
	require.Equal(t, int32(100), monitor.checks.Load())
	require.Equal(t, int32(100), monitor.notices.Load())
	require.Zero(t, cache.sweeps.Load())

	server.handleInactivityMonitors(time.Now())
	require.Equal(t, int32(101), monitor.checks.Load())
	require.Equal(t, int32(1), cache.sweeps.Load())
}
