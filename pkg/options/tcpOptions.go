package options

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	tcpClient "github.com/plgd-dev/go-coap/v2/tcp/client"
	tcpServer "github.com/plgd-dev/go-coap/v2/tcp/server"
	udpClient "github.com/plgd-dev/go-coap/v2/udp/client"
)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

// OnNewClientConnFunc is the callback for new connections.
//
// Note: Calling `tlscon.Close()` is forbidden, and `tlscon` should be treated as a
// "read-only" parameter, mainly used to get the peer certificate from the underlining connection
type OnNewClientConnFunc = func(cc *tcpClient.ClientConn, tlscon *tls.Conn)

type Handler interface {
	tcpClient.HandlerFunc | udpClient.HandlerFunc
}

// HandlerFuncOpt handler function option.
type HandlerFuncOpt[H Handler] struct {
	h H
}

func (o HandlerFuncOpt[H]) TCPServerApply(cfg *tcpServer.Config) {
	switch v := any(o.h).(type) {
	case tcpClient.HandlerFunc:
		cfg.Handler = v
	default:
		var t tcpClient.HandlerFunc
		panic(fmt.Errorf("invalid HandlerFunc type %T, expected %T", v, t))
	}
}

func (o HandlerFuncOpt[H]) TCPClientApply(cfg *tcpClient.Config) {
	switch v := any(o.h).(type) {
	case tcpClient.HandlerFunc:
		cfg.Handler = v
	default:
		var t tcpClient.HandlerFunc
		panic(fmt.Errorf("invalid HandlerFunc type %T, expected %T", v, t))
	}
}

// WithHandlerFunc set handle for handling request's.
func WithHandlerFunc[H Handler](h H) HandlerFuncOpt[H] {
	return HandlerFuncOpt[H]{h: h}
}

// HandlerFuncOpt handler function option.
type MuxHandlerOpt struct {
	m mux.Handler
}

func (o MuxHandlerOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.Handler = mux.ToHandler[*tcpClient.ClientConn](o.m)
}

func (o MuxHandlerOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.Handler = mux.ToHandler[*tcpClient.ClientConn](o.m)
}

// WithMux set's multiplexer for handle requests.
func WithMux(m mux.Handler) MuxHandlerOpt {
	return MuxHandlerOpt{
		m: m,
	}
}

// ContextOpt handler function option.
type ContextOpt struct {
	ctx context.Context
}

func (o ContextOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.Ctx = o.ctx
}

func (o ContextOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.Ctx = o.ctx
}

// WithContext set's parent context of server.
func WithContext(ctx context.Context) ContextOpt {
	return ContextOpt{ctx: ctx}
}

// MaxMessageSizeOpt handler function option.
type MaxMessageSizeOpt struct {
	maxMessageSize uint32
}

func (o MaxMessageSizeOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.MaxMessageSize = o.maxMessageSize
}

func (o MaxMessageSizeOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.MaxMessageSize = o.maxMessageSize
}

// WithMaxMessageSize limit size of processed message.
func WithMaxMessageSize(maxMessageSize uint32) MaxMessageSizeOpt {
	return MaxMessageSizeOpt{maxMessageSize: maxMessageSize}
}

// ErrorsOpt errors option.
type ErrorsOpt struct {
	errors ErrorFunc
}

func (o ErrorsOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.Errors = o.errors
}

func (o ErrorsOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.Errors = o.errors
}

// WithErrors set function for logging error.
func WithErrors(errors ErrorFunc) ErrorsOpt {
	return ErrorsOpt{errors: errors}
}

// GoPoolOpt gopool option.
type GoPoolOpt struct {
	goPool GoPoolFunc
}

func (o GoPoolOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.GoPool = o.goPool
}

func (o GoPoolOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.GoPool = o.goPool
}

// WithGoPool sets function for managing spawning go routines
// for handling incoming request's.
// Eg: https://github.com/panjf2000/ants.
func WithGoPool(goPool GoPoolFunc) GoPoolOpt {
	return GoPoolOpt{goPool: goPool}
}

// KeepAliveOpt keepalive option.
type KeepAliveOpt struct {
	timeout    time.Duration
	onInactive inactivity.OnInactiveFunc
	maxRetries uint32
}

func (o KeepAliveOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.CreateInactivityMonitor = func() inactivity.Monitor {
		keepalive := inactivity.NewKeepAlive(o.maxRetries, o.onInactive, func(cc inactivity.ClientConn, receivePong func()) (func(), error) {
			return cc.(*tcpClient.ClientConn).AsyncPing(receivePong)
		})
		return inactivity.NewInactivityMonitor(o.timeout/time.Duration(o.maxRetries+1), keepalive.OnInactive)
	}
}

func (o KeepAliveOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.CreateInactivityMonitor = func() inactivity.Monitor {
		keepalive := inactivity.NewKeepAlive(o.maxRetries, o.onInactive, func(cc inactivity.ClientConn, receivePong func()) (func(), error) {
			return cc.(*tcpClient.ClientConn).AsyncPing(receivePong)
		})
		return inactivity.NewInactivityMonitor(o.timeout/time.Duration(o.maxRetries+1), keepalive.OnInactive)
	}
}

// WithKeepAlive monitoring's client connection's.
func WithKeepAlive(maxRetries uint32, timeout time.Duration, onInactive inactivity.OnInactiveFunc) KeepAliveOpt {
	return KeepAliveOpt{
		maxRetries: maxRetries,
		timeout:    timeout,
		onInactive: onInactive,
	}
}

// InactivityMonitorOpt notifies when a connection was inactive for a given duration.
type InactivityMonitorOpt struct {
	duration   time.Duration
	onInactive inactivity.OnInactiveFunc
}

func (o InactivityMonitorOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.CreateInactivityMonitor = func() inactivity.Monitor {
		return inactivity.NewInactivityMonitor(o.duration, o.onInactive)
	}
}

func (o InactivityMonitorOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.CreateInactivityMonitor = func() inactivity.Monitor {
		return inactivity.NewInactivityMonitor(o.duration, o.onInactive)
	}
}

// WithInactivityMonitor set deadline's for read operations over client connection.
func WithInactivityMonitor(duration time.Duration, onInactive inactivity.OnInactiveFunc) InactivityMonitorOpt {
	return InactivityMonitorOpt{
		duration:   duration,
		onInactive: onInactive,
	}
}

// NetOpt network option.
type NetOpt struct {
	net string
}

func (o NetOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.Net = o.net
}

// WithNetwork define's tcp version (udp4, udp6, tcp) for client.
func WithNetwork(net string) NetOpt {
	return NetOpt{net: net}
}

// PeriodicRunnerOpt function which is executed in every ticks
type PeriodicRunnerOpt struct {
	periodicRunner periodic.Func
}

func (o PeriodicRunnerOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.PeriodicRunner = o.periodicRunner
}

func (o PeriodicRunnerOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.PeriodicRunner = o.periodicRunner
}

// WithPeriodicRunner set function which is executed in every ticks.
func WithPeriodicRunner(periodicRunner periodic.Func) PeriodicRunnerOpt {
	return PeriodicRunnerOpt{periodicRunner: periodicRunner}
}

// BlockwiseOpt network option.
type BlockwiseOpt struct {
	transferTimeout time.Duration
	enable          bool
	szx             blockwise.SZX
}

func (o BlockwiseOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.BlockwiseEnable = o.enable
	cfg.BlockwiseSZX = o.szx
	cfg.BlockwiseTransferTimeout = o.transferTimeout
}

func (o BlockwiseOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.BlockwiseEnable = o.enable
	cfg.BlockwiseSZX = o.szx
	cfg.BlockwiseTransferTimeout = o.transferTimeout
}

// WithBlockwise configure's blockwise transfer.
func WithBlockwise(enable bool, szx blockwise.SZX, transferTimeout time.Duration) BlockwiseOpt {
	return BlockwiseOpt{
		enable:          enable,
		szx:             szx,
		transferTimeout: transferTimeout,
	}
}

// OnNewClientConnOpt network option.
type OnNewClientConnOpt struct {
	onNewClientConn OnNewClientConnFunc
}

func (o OnNewClientConnOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.OnNewClientConn = o.onNewClientConn
}

// WithOnNewClientConn server's notify about new client connection.
//
// Note: Calling `tlscon.Close()` is forbidden, and `tlscon` should be treated as a
// "read-only" parameter, mainly used to get the peer certificate from the underlining connection
func WithOnNewClientConn(onNewClientConn OnNewClientConnFunc) OnNewClientConnOpt {
	return OnNewClientConnOpt{
		onNewClientConn: onNewClientConn,
	}
}

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

// WithDisableTCPSignalMessageCSM don't send CSM when client conn is created.
func WithDisableTCPSignalMessageCSM() DisableTCPSignalMessageCSMOpt {
	return DisableTCPSignalMessageCSMOpt{}
}

// TLSOpt tls configuration option.
type TLSOpt struct {
	tlsCfg *tls.Config
}

func (o TLSOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.TlsCfg = o.tlsCfg
}

// WithTLS creates tls connection.
func WithTLS(cfg *tls.Config) TLSOpt {
	return TLSOpt{
		tlsCfg: cfg,
	}
}

// CloseSocketOpt close socket option.
type CloseSocketOpt struct{}

func (o CloseSocketOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.CloseSocket = true
}

// WithCloseSocket closes socket at the close connection.
func WithCloseSocket() CloseSocketOpt {
	return CloseSocketOpt{}
}

// DialerOpt dialer option.
type DialerOpt struct {
	dialer *net.Dialer
}

func (o DialerOpt) TCPClientApply(cfg *tcpClient.Config) {
	if o.dialer != nil {
		cfg.Dialer = o.dialer
	}
}

// WithDialer set dialer for dial.
func WithDialer(dialer *net.Dialer) DialerOpt {
	return DialerOpt{
		dialer: dialer,
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

// ConnectionCacheOpt network option.
type MessagePoolOpt struct {
	messagePool *pool.Pool
}

func (o MessagePoolOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.MessagePool = o.messagePool
}

func (o MessagePoolOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.MessagePool = o.messagePool
}

// WithMessagePool configure's message pool for acquire/releasing coap messages
func WithMessagePool(messagePool *pool.Pool) MessagePoolOpt {
	return MessagePoolOpt{
		messagePool: messagePool,
	}
}

// GetTokenOpt token option.
type GetTokenOpt struct {
	getToken client.GetTokenFunc
}

func (o GetTokenOpt) TCPServerApply(cfg *tcpServer.Config) {
	cfg.GetToken = o.getToken
}

func (o GetTokenOpt) TCPClientApply(cfg *tcpClient.Config) {
	cfg.GetToken = o.getToken
}

// WithGetToken set function for generating tokens.
func WithGetToken(getToken client.GetTokenFunc) GetTokenOpt {
	return GetTokenOpt{getToken: getToken}
}
