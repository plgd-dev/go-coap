package options_test

import (
	"testing"
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/options"
	tcpServer "github.com/plgd-dev/go-coap/v3/tcp/server"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
)

func TestMaxPendingMessagesDefaultsAndDisable(t *testing.T) {
	clientCfg := client.DefaultConfig
	udpCfg := udpServer.DefaultConfig
	dtlsCfg := dtlsServer.DefaultConfig
	require.Equal(t, uint32(1024), clientCfg.MaxPendingMessages)
	require.Equal(t, client.DefaultMaxPendingMessages, udpCfg.MaxPendingMessages)
	require.Equal(t, client.DefaultMaxPendingMessages, dtlsCfg.MaxPendingMessages)
	opt := options.WithMaxPendingMessages(0)
	opt.UDPClientApply(&clientCfg)
	opt.UDPServerApply(&udpCfg)
	opt.DTLSServerApply(&dtlsCfg)
	require.Zero(t, clientCfg.MaxPendingMessages)
	require.Zero(t, udpCfg.MaxPendingMessages)
	require.Zero(t, dtlsCfg.MaxPendingMessages)
}

func TestEndpointMessageIDCacheOption(t *testing.T) {
	cfg := udpServer.DefaultConfig
	defaults := options.DefaultEndpointMessageIDCacheConfig()
	require.Equal(t, uint32(4096), defaults.MaxEntries)
	require.Equal(t, options.EndpointMessageIDFailOpen, defaults.OverflowMode)
	require.Equal(t, defaults, cfg.EndpointMessageIDCache)
	custom := defaults
	custom.MaxEntries = 2
	custom.OverflowMode = options.EndpointMessageIDFailClosed
	opt := options.WithEndpointMessageIDCache(custom)
	custom.MaxEntries = 3
	opt.UDPServerApply(&cfg)
	require.Equal(t, uint32(2), cfg.EndpointMessageIDCache.MaxEntries)
	require.Equal(t, options.EndpointMessageIDFailClosed, cfg.EndpointMessageIDCache.OverflowMode)
	require.Equal(t, defaults, options.DefaultEndpointMessageIDCacheConfig())
	options.WithEndpointMessageIDCache(options.EndpointMessageIDCacheConfig{}).UDPServerApply(&cfg)
	require.Zero(t, cfg.EndpointMessageIDCache.MaxEntries)
	require.False(t, cfg.EndpointMessageIDCache.Unlimited)
	require.Equal(t, options.EndpointMessageIDFailOpen, cfg.EndpointMessageIDCache.OverflowMode)
	options.WithEndpointMessageIDCache(options.EndpointMessageIDCacheConfig{Unlimited: true}).UDPServerApply(&cfg)
	require.Zero(t, cfg.EndpointMessageIDCache.MaxEntries)
	require.True(t, cfg.EndpointMessageIDCache.Unlimited)
}

func TestUDPServerApply(t *testing.T) {
	require.Equal(t, 5*time.Second, udpServer.DefaultConfig.WriteTimeout)
	cfg := udpServer.Config{}
	opt := []udpServer.Option{
		options.WithTransmission(10, time.Second, 5, 2.0),
		options.WithMTU(1500),
		options.WithMaxPendingMessages(7),
		options.WithServerWriteTimeout(3 * time.Second),
		options.WithEndpointMessageIDCache(options.EndpointMessageIDCacheConfig{
			MaxEntries: 8, OverflowMode: options.EndpointMessageIDFailClosed,
		}),
	}
	for _, o := range opt {
		o.UDPServerApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.InDelta(t, 2.0, cfg.TransmissionAcknowledgeRandomFactor, 1e-9)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
	require.Equal(t, uint32(7), cfg.MaxPendingMessages)
	require.Equal(t, 3*time.Second, cfg.WriteTimeout)
	require.Equal(t, uint32(8), cfg.EndpointMessageIDCache.MaxEntries)
	require.Equal(t, options.EndpointMessageIDFailClosed, cfg.EndpointMessageIDCache.OverflowMode)
}

func TestServerWriteTimeoutOption(t *testing.T) {
	require.Equal(t, 5*time.Second, tcpServer.DefaultConfig.WriteTimeout)
	require.Equal(t, 5*time.Second, udpServer.DefaultConfig.WriteTimeout)
	require.Equal(t, 5*time.Second, dtlsServer.DefaultConfig.WriteTimeout)

	opt := options.WithServerWriteTimeout(3 * time.Second)
	tcpCfg := tcpServer.Config{}
	udpCfg := udpServer.Config{}
	dtlsCfg := dtlsServer.Config{}
	opt.TCPServerApply(&tcpCfg)
	opt.UDPServerApply(&udpCfg)
	opt.DTLSServerApply(&dtlsCfg)
	require.Equal(t, 3*time.Second, tcpCfg.WriteTimeout)
	require.Equal(t, 3*time.Second, udpCfg.WriteTimeout)
	require.Equal(t, 3*time.Second, dtlsCfg.WriteTimeout)
}

func TestDTLSServerApply(t *testing.T) {
	cfg := dtlsServer.Config{}
	require.Equal(t, uint32(1024), dtlsServer.DefaultConfig.MaxPendingHandshakes)
	opt := []dtlsServer.Option{
		options.WithTransmission(10, time.Second, 5, 2.0),
		options.WithMTU(1500),
		options.WithDTLSHandshakeTimeout(12 * time.Second),
		options.WithDTLSMaxPendingHandshakes(42),
		options.WithMaxPendingMessages(7),
	}
	for _, o := range opt {
		o.DTLSServerApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.InDelta(t, 2.0, cfg.TransmissionAcknowledgeRandomFactor, 1e-9)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
	// WithDTLSHandshakeTimeout
	require.Equal(t, 12*time.Second, cfg.HandshakeTimeout)
	require.Equal(t, uint32(42), cfg.MaxPendingHandshakes)
	require.Equal(t, uint32(7), cfg.MaxPendingMessages)
	options.WithDTLSMaxPendingHandshakes(0).DTLSServerApply(&cfg)
	require.Zero(t, cfg.MaxPendingHandshakes)
}

func TestUDPClientApply(t *testing.T) {
	cfg := client.Config{}
	opt := []udp.Option{
		options.WithTransmission(10, time.Second, 5, 2.0),
		options.WithMTU(1500),
		options.WithMaxPendingMessages(7),
	}
	for _, o := range opt {
		o.UDPClientApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.InDelta(t, 2.0, cfg.TransmissionAcknowledgeRandomFactor, 1e-9)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
	require.Equal(t, uint32(7), cfg.MaxPendingMessages)
}
