package options_test

import (
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
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
