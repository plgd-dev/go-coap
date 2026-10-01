package options_test

import (
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
	"reflect"
	"testing"
)

func TestQBlockServerOption(t *testing.T) {
	c := udpServer.DefaultConfig
	q := qblock.DefaultServerConfig()
	options.WithQBlockServer(q).UDPServerApply(&c)
	require.Equal(t, q, *c.QBlockServer)
}

func TestQBlockOptionCopies(t *testing.T) {
	c := udpServer.DefaultConfig
	q := qblock.DefaultClientConfig()
	o := options.WithQBlock(q)
	q.Mode = qblock.Require
	o.UDPServerApply(&c)
	require.Equal(t, qblock.PreferKnown, c.QBlock.Mode)
	other := udpServer.DefaultConfig
	o.UDPServerApply(&other)
	c.QBlock.MaxProbeWaiters = 1
	require.Equal(t, uint32(64), other.QBlock.MaxProbeWaiters)
}

func TestQBlockOptionOrderSharedMismatch(t *testing.T) {
	base := qblock.DefaultClientConfig()
	server := qblock.DefaultServerConfig()
	for _, field := range []string{"Manager", "ProbingRate", "NonProbingWait", "MaxIntentBytes", "MaxOwnedBytes", "MaxMIDEntries", "MaxPeers", "MaxConnections", "MaxEndpointMembers"} {
		changed := base
		value := reflect.ValueOf(&changed).Elem().FieldByName(field)
		if field == "Manager" {
			changed.Manager.MaxTokens++
		} else if value.Kind() == reflect.Int64 {
			value.SetInt(1)
		} else {
			value.SetUint(value.Uint() + 1)
		}
		for _, reverse := range []bool{false, true} {
			cfg := udpServer.DefaultConfig
			if reverse {
				options.WithQBlockServer(server).UDPServerApply(&cfg)
				options.WithQBlock(changed).UDPServerApply(&cfg)
			} else {
				options.WithQBlock(changed).UDPServerApply(&cfg)
				options.WithQBlockServer(server).UDPServerApply(&cfg)
			}
			_, err := udpClient.NewQBlockRuntime(cfg.QBlock, cfg.QBlockServer)
			require.Error(t, err, field)
		}
	}
}
