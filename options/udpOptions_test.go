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

func TestUDPServerApply(t *testing.T) {
	cfg := udpServer.Config{}
	opt := []udpServer.Option{
		options.WithTransmission(10, time.Second, 5),
		options.WithMTU(1500),
	}
	for _, o := range opt {
		o.UDPServerApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
}

func TestDTLSServerApply(t *testing.T) {
	cfg := dtlsServer.Config{}
	opt := []dtlsServer.Option{
		options.WithTransmission(10, time.Second, 5),
		options.WithMTU(1500),
	}
	for _, o := range opt {
		o.DTLSServerApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
}

func TestUDPClientApply(t *testing.T) {
	cfg := client.Config{}
	opt := []udp.Option{
		options.WithTransmission(10, time.Second, 5),
		options.WithMTU(1500),
	}
	for _, o := range opt {
		o.UDPClientApply(&cfg)
	}
	// WithTransmission
	require.Equal(t, uint32(10), cfg.TransmissionNStart)
	require.Equal(t, time.Second, cfg.TransmissionAcknowledgeTimeout)
	require.Equal(t, uint32(5), cfg.TransmissionMaxRetransmit)
	// WithMTU
	require.Equal(t, uint16(1500), cfg.MTU)
}
