package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	tcpClient "github.com/plgd-dev/go-coap/v2/tcp/client"
)

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type TCPServerOption interface {
	TCPServerApply(*Config)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*responsewriter.ResponseWriter[*tcpClient.ClientConn], *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

// OnNewClientConnFunc is the callback for new connections.
//
// Note: Calling `tlscon.Close()` is forbidden, and `tlscon` should be treated as a
// "read-only" parameter, mainly used to get the peer certificate from the underlining connection
type OnNewClientConnFunc = func(cc *tcpClient.ClientConn, tlscon *tls.Conn)

var DefaultConfig = func() Config {
	opts := Config{
		Ctx:            context.Background(),
		MaxMessageSize: 64 * 1024,
		Errors: func(err error) {
			fmt.Println(err)
		},
		GoPool: func(f func()) error {
			go func() {
				f()
			}()
			return nil
		},
		BlockwiseEnable:          true,
		BlockwiseSZX:             blockwise.SZX1024,
		BlockwiseTransferTimeout: time.Second * 3,
		OnNewClientConn: func(cc *tcpClient.ClientConn, tlscon *tls.Conn) {
			// do nothing by default
		},
		CreateInactivityMonitor: func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		},
		PeriodicRunner: func(f func(now time.Time) bool) {
			go func() {
				for f(time.Now()) {
					time.Sleep(4 * time.Second)
				}
			}()
		},
		ConnectionCacheSize: 2 * 1024,
		MessagePool:         pool.New(1024, 2048),
		GetToken:            message.GetToken,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*tcpClient.ClientConn], r *pool.Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			opts.Errors(fmt.Errorf("server handler: cannot set response: %w", err))
		}
	}
	return opts
}()

type Config struct {
	Ctx                             context.Context
	MessagePool                     *pool.Pool
	Handler                         HandlerFunc
	Errors                          ErrorFunc
	GoPool                          GoPoolFunc
	CreateInactivityMonitor         func() inactivity.Monitor
	PeriodicRunner                  periodic.Func
	OnNewClientConn                 OnNewClientConnFunc
	BlockwiseTransferTimeout        time.Duration
	MaxMessageSize                  uint32
	ConnectionCacheSize             uint16
	DisablePeerTCPSignalMessageCSMs bool
	DisableTCPSignalMessageCSM      bool
	BlockwiseSZX                    blockwise.SZX
	BlockwiseEnable                 bool
	GetToken                        client.GetTokenFunc
}
