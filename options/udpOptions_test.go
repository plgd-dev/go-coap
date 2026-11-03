package options_test

import (
	"testing"
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/options"
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

func TestUDPServerApply(t *testing.T) {
	cfg := udpServer.Config{}
	opt := []udpServer.Option{
		options.WithTransmission(10, time.Second, 5, 2.0),
		options.WithMTU(1500),
		options.WithMaxPendingMessages(7),
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
}

func TestDTLSServerApply(t *testing.T) {
	cfg := dtlsServer.Config{}
	opt := []dtlsServer.Option{
		options.WithTransmission(10, time.Second, 5, 2.0),
		options.WithMTU(1500),
		options.WithDTLSHandshakeTimeout(12 * time.Second),
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
	require.Equal(t, uint32(7), cfg.MaxPendingMessages)
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
