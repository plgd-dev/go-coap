package options

import (
	"crypto/tls"
	"time"

	tcpClient "github.com/plgd-dev/go-coap/v3/tcp/client"
	tcpServer "github.com/plgd-dev/go-coap/v3/tcp/server"
)

// DisablePeerTCPSignalMessageCSMsOpt coap-tcp csm option.
type DisablePeerTCPSignalMessageCSMsOpt struct{}

func (o DisablePeerTCPSignalMessageCSMsOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.DisablePeerTCPSignalMessageCSMs = true
}

func (o DisablePeerTCPSignalMessageCSMsOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.DisablePeerTCPSignalMessageCSMs = true
}

// WithDisablePeerTCPSignalMessageCSMs ignor peer's CSM message.
func WithDisablePeerTCPSignalMessageCSMs() DisablePeerTCPSignalMessageCSMsOpt {
	return DisablePeerTCPSignalMessageCSMsOpt{}
}

// DisableTCPSignalMessageCSMOpt coap-tcp csm option.
type DisableTCPSignalMessageCSMOpt struct{}

func (o DisableTCPSignalMessageCSMOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.DisableTCPSignalMessageCSM = true
}

func (o DisableTCPSignalMessageCSMOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.DisableTCPSignalMessageCSM = true
}

func WithDisableTCPSignalMessageCSM() DisableTCPSignalMessageCSMOpt {
	return DisableTCPSignalMessageCSMOpt{}
}

type CSMExchangeTimeoutOpt struct {
	timeout time.Duration
}

func (o CSMExchangeTimeoutOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.CSMExchangeTimeout = o.timeout
}

func WithCSMExchangeTimeout(timeout time.Duration) CSMExchangeTimeoutOpt {
	return CSMExchangeTimeoutOpt{
		timeout: timeout,
	}
}

// TLSOpt tls configuration option.
type TLSOpt struct {
	tlsCfg *tls.Config
}

func (o TLSOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.TLSCfg = o.tlsCfg
}

// WithTLS creates tls connection.
func WithTLS(cfg *tls.Config) TLSOpt {
	return TLSOpt{
		tlsCfg: cfg,
	}
}

// ConnectionCacheOpt network option.
type ConnectionCacheSizeOpt struct {
	connectionCacheSize uint16
}

func (o ConnectionCacheSizeOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.ConnectionCacheSize = o.connectionCacheSize
}

func (o ConnectionCacheSizeOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.ConnectionCacheSize = o.connectionCacheSize
}

// WithConnectionCacheSize configure's maximum size of cache of read buffer.
func WithConnectionCacheSize(connectionCacheSize uint16) ConnectionCacheSizeOpt {
	return ConnectionCacheSizeOpt{
		connectionCacheSize: connectionCacheSize,
	}
}
