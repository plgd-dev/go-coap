package server

import (
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
)

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type TCPServerOption interface {
	TCPServerApply(*Config)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*responsewriter.ResponseWriter[*client.ClientConn], *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

// OnNewClientConnFunc is the callback for new connections.
type OnNewClientConnFunc = func(cc *client.ClientConn)

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.DefaultCommon(),
		CreateInactivityMonitor: func() client.InactivityMonitor {
			maxRetries := uint32(2)
			timeout := time.Second * 16
			onInactive := func(cc *client.ClientConn) {
				_ = cc.Close()
			}
			keepalive := inactivity.NewKeepAlive(maxRetries, onInactive, func(cc *client.ClientConn, receivePong func()) (func(), error) {
				return cc.AsyncPing(receivePong)
			})
			return inactivity.NewInactivityMonitor(timeout/time.Duration(maxRetries+1), keepalive.OnInactive)
		},
		OnNewClientConn: func(cc *client.ClientConn) {
			// do nothing by default
		},
		ConnectionCacheSize: 2 * 1024,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*client.ClientConn], r *pool.Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			opts.Errors(fmt.Errorf("server handler: cannot set response: %w", err))
		}
	}
	return opts
}()

type Config struct {
	config.Common
	CreateInactivityMonitor         client.CreateInactivityMonitorFunc
	Handler                         HandlerFunc
	OnNewClientConn                 OnNewClientConnFunc
	ConnectionCacheSize             uint16
	DisablePeerTCPSignalMessageCSMs bool
	DisableTCPSignalMessageCSM      bool
}
