package server

import (
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
	"net"
	"testing"
)

func TestQBlockServerConnectionLimitAllowsWildcardFallback(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxConnections = 1
	srv := New()
	srv.cfg.QBlockServer = &cfg
	remote := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 123}
	local := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 456}
	existing := &client.Conn{}
	srv.conns[getConnKey(remote, toWildcardLocalAddr(local))] = existing
	got, created, err := srv.getOrCreateConn(nil, remote, local)
	require.NoError(t, err)
	require.False(t, created)
	require.Same(t, existing, got)
}
