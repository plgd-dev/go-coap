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
