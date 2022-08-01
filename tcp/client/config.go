package client

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/options/config"
)

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.DefaultCommon(func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		}),
		Dialer:              &net.Dialer{Timeout: time.Second * 3},
		Net:                 "tcp",
		ConnectionCacheSize: 2048,
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
	config.Common
	Net                             string
	Dialer                          *net.Dialer
	TLSCfg                          *tls.Config
	Handler                         HandlerFunc
	ConnectionCacheSize             uint16
	DisablePeerTCPSignalMessageCSMs bool
	CloseSocket                     bool
	DisableTCPSignalMessageCSM      bool
}
