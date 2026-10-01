package options

import (
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
)

// QBlockServerOpt enables server-side NON Q-Block handling on UDP only.
type QBlockServerOpt struct{ config qblock.ServerConfig }

func (o QBlockServerOpt) UDPServerApply(cfg *udpServer.Config) {
	copy := o.config
	cfg.QBlockServer = &copy
}

// WithQBlockServer enables GET responses and assembled POST/PUT handlers.
// Start from qblock.DefaultServerConfig and adjust explicit resource limits.
func WithQBlockServer(cfg qblock.ServerConfig) QBlockServerOpt { return QBlockServerOpt{config: cfg} }
