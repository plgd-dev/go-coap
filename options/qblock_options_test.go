package options_test

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/dtls"
	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
)

func TestQBlockwiseOptionsApplyToDatagramConfigs(t *testing.T) {
	clientCfg := client.DefaultConfig
	udpServerCfg := udpServer.DefaultConfig
	dtlsServerCfg := dtlsServer.DefaultConfig

	mode := options.WithQBlockwise(options.QBlockwiseOnly)
	mode.UDPClientApply(&clientCfg)
	mode.UDPServerApply(&udpServerCfg)
	mode.DTLSServerApply(&dtlsServerCfg)

	require.Equal(t, blockwise.QBlockwiseOnly, clientCfg.QBlockwiseMode)
	require.Equal(t, blockwise.QBlockwiseOnly, udpServerCfg.QBlockwiseMode)
	require.Equal(t, blockwise.QBlockwiseOnly, dtlsServerCfg.QBlockwiseMode)
}

func TestQBlockwiseParametersApplyToDatagramConfigs(t *testing.T) {
	parameters := qblock.DefaultConfig()
	parameters.MaxPayloads = 3
	parameters.NonMaxRetransmit = 0
	option := options.WithQBlockwiseConfig(parameters)
	clientCfg, udpServerCfg, dtlsServerCfg := client.DefaultConfig, udpServer.DefaultConfig, dtlsServer.DefaultConfig
	option.UDPClientApply(&clientCfg)
	option.UDPServerApply(&udpServerCfg)
	option.DTLSServerApply(&dtlsServerCfg)
	require.Equal(t, parameters, clientCfg.QBlockwiseConfig)
	require.Equal(t, parameters, udpServerCfg.QBlockwiseConfig)
	require.Equal(t, parameters, dtlsServerCfg.QBlockwiseConfig)
	clientCfg.QBlockwiseConfig.MaxPayloads = 16
	require.EqualValues(t, 3, udpServerCfg.QBlockwiseConfig.MaxPayloads)
	require.EqualValues(t, 3, dtlsServerCfg.QBlockwiseConfig.MaxPayloads)
}

func TestQBlockwiseInvalidParametersRejectStartup(t *testing.T) {
	parameters := qblock.DefaultConfig()
	parameters.MaxPayloads = 257
	option := options.WithQBlockwiseConfig(parameters)
	// Invalid configuration must be rejected before resolving a target or using a listener.
	cc, err := udp.Dial("invalid target", option)
	require.Nil(t, cc)
	require.ErrorIs(t, err, qblock.ErrInvalidConfig)
	cc, err = dtls.Dial("invalid target", dtls.NewDTLSClientOptions(), option)
	require.Nil(t, cc)
	require.ErrorIs(t, err, qblock.ErrInvalidConfig)
	require.ErrorIs(t, udpServer.New(option).Serve(nil), qblock.ErrInvalidConfig)
	require.ErrorIs(t, dtlsServer.New(option).Serve(nil), qblock.ErrInvalidConfig)
}

func TestQBlockwiseConfigModeValidation(t *testing.T) {
	require.NoError(t, blockwise.ValidateQBlockwiseConfig(blockwise.QBlockwiseDisabled, false))
	require.NoError(t, blockwise.ValidateQBlockwiseConfig(blockwise.QBlockwiseAuto, true))
	require.NoError(t, blockwise.ValidateQBlockwiseConfig(blockwise.QBlockwiseOnly, true))
	require.ErrorIs(t, blockwise.ValidateQBlockwiseConfig(blockwise.QBlockwiseAuto, false), blockwise.ErrQBlockNeedsBlockwise)
	require.ErrorIs(t, blockwise.ValidateQBlockwiseConfig(blockwise.QBlockwiseMode(3), true), blockwise.ErrQBlockInvalidMode)
}
