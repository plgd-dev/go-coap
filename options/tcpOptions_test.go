package options_test

import (
	"crypto/tls"
	"testing"
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/plgd-dev/go-coap/v3/tcp/server"
	"github.com/stretchr/testify/require"
)

func TestTCPClientApply(t *testing.T) {
	cfg := client.Config{}
	tlsCfg := &tls.Config{}
	opt := []tcp.Option{
		options.WithDisablePeerTCPSignalMessageCSMs(),
		options.WithDisableTCPSignalMessageCSM(),
		options.WithTLS(tlsCfg),
		options.WithConnectionCacheSize(100),
	}
	for _, o := range opt {
		o.TCPClientApply(&cfg)
	}
	require.True(t, cfg.DisablePeerTCPSignalMessageCSMs)
	require.True(t, cfg.DisableTCPSignalMessageCSM)
	require.Equal(t, tlsCfg, cfg.TLSCfg)
	require.Equal(t, uint16(100), cfg.ConnectionCacheSize)
}

func TestTCPServerApply(t *testing.T) {
	cfg := server.Config{}
	opt := []server.Option{
		options.WithDisablePeerTCPSignalMessageCSMs(),
		options.WithDisableTCPSignalMessageCSM(),
		options.WithConnectionCacheSize(100),
	}
	for _, o := range opt {
		o.TCPServerApply(&cfg)
	}
	require.True(t, cfg.DisablePeerTCPSignalMessageCSMs)
	require.True(t, cfg.DisableTCPSignalMessageCSM)
	require.Equal(t, uint16(100), cfg.ConnectionCacheSize)
}

func TestServerHandshakeOptions(t *testing.T) {
	tcpCfg := server.DefaultConfig
	dtlsCfg := dtlsServer.DefaultConfig
	require.Equal(t, 30*time.Second, tcpCfg.HandshakeTimeout)
	require.Equal(t, dtlsCfg.HandshakeTimeout, tcpCfg.HandshakeTimeout)
	require.Equal(t, uint32(1024), tcpCfg.MaxPendingHandshakes)
	require.Equal(t, dtlsCfg.MaxPendingHandshakes, tcpCfg.MaxPendingHandshakes)
	timeout := options.WithHandshakeTimeout(5 * time.Second)
	limit := options.WithMaxPendingHandshakes(5)
	timeout.TCPServerApply(&tcpCfg)
	timeout.DTLSServerApply(&dtlsCfg)
	limit.TCPServerApply(&tcpCfg)
	limit.DTLSServerApply(&dtlsCfg)
	require.Equal(t, 5*time.Second, tcpCfg.HandshakeTimeout)
	require.Equal(t, tcpCfg.HandshakeTimeout, dtlsCfg.HandshakeTimeout)
	require.Equal(t, uint32(5), tcpCfg.MaxPendingHandshakes)
	require.Equal(t, tcpCfg.MaxPendingHandshakes, dtlsCfg.MaxPendingHandshakes)
	options.WithHandshakeTimeout(0).TCPServerApply(&tcpCfg)
	options.WithMaxPendingHandshakes(0).TCPServerApply(&tcpCfg)
	require.Zero(t, tcpCfg.HandshakeTimeout)
	require.Zero(t, tcpCfg.MaxPendingHandshakes)
}
