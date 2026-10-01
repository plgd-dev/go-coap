package options

import (
	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
)

// QBlockServerOpt enables server-side NON Q-Block handling on UDP and DTLS.
type QBlockServerOpt struct{ config qblock.ServerConfig }

func (o QBlockServerOpt) UDPServerApply(cfg *udpServer.Config) {
	copy := o.config
	cfg.QBlockServer = &copy
}

// WithQBlockServer enables GET responses and assembled POST/PUT handlers.
// Start from qblock.DefaultServerConfig and adjust explicit resource limits.
func WithQBlockServer(cfg qblock.ServerConfig) QBlockServerOpt { return QBlockServerOpt{config: cfg} }

func (o QBlockServerOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	copy := o.config
	cfg.QBlockServer = &copy
}

// QBlockConfig and Mode are the canonical outbound configuration aliases.
type QBlockConfig = qblock.ClientConfig
type Mode = qblock.Mode

const (
	PreferKnown = qblock.PreferKnown
	Require     = qblock.Require
)

type QBlockOpt struct{ config qblock.ClientConfig }

// WithQBlock enables outbound selection after explicit peer discovery.
func WithQBlock(c qblock.ClientConfig) QBlockOpt         { return QBlockOpt{c} }
func (o QBlockOpt) UDPClientApply(c *udpClient.Config)   { copy := o.config; c.QBlock = &copy }
func (o QBlockOpt) UDPServerApply(c *udpServer.Config)   { copy := o.config; c.QBlock = &copy }
func (o QBlockOpt) DTLSServerApply(c *dtlsServer.Config) { copy := o.config; c.QBlock = &copy }
