package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
)

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
		Dialer:                   &net.Dialer{Timeout: time.Second * 3},
		Net:                      "tcp",
		BlockwiseSZX:             blockwise.SZX1024,
		BlockwiseEnable:          true,
		BlockwiseTransferTimeout: time.Second * 3,
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
		ConnectionCacheSize: 2048,
		MessagePool:         pool.New(1024, 2048),
		GetToken:            message.GetToken,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
				opts.Errors(fmt.Errorf("client handler: cannot set response: %w", err))
			}
		}
	}
	return opts
}()

type Config struct {
	Ctx                             context.Context
	Net                             string
	BlockwiseTransferTimeout        time.Duration
	MessagePool                     *pool.Pool
	GoPool                          GoPoolFunc
	Dialer                          *net.Dialer
	TlsCfg                          *tls.Config
	PeriodicRunner                  periodic.Func
	CreateInactivityMonitor         func() inactivity.Monitor
	Handler                         HandlerFunc
	Errors                          ErrorFunc
	GetToken                        client.GetTokenFunc
	MaxMessageSize                  uint32
	ConnectionCacheSize             uint16
	DisablePeerTCPSignalMessageCSMs bool
	CloseSocket                     bool
	BlockwiseEnable                 bool
	BlockwiseSZX                    blockwise.SZX
	DisableTCPSignalMessageCSM      bool
}
