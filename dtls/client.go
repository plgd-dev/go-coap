package dtls

import (
	"fmt"
	"time"

	"github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/plgd-dev/go-coap/v3/udp"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
)

var DefaultConfig = func() udpClient.Config {
	cfg := udpClient.DefaultConfig
	cfg.Handler = func(w *responsewriter.ResponseWriter[*udpClient.ClientConn], r *pool.Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
				cfg.Errors(fmt.Errorf("dtls client: cannot set response: %w", err))
			}
		}
	}
	return cfg
}()

// Dial creates a client connection to the given target.
func Dial(target string, dtlsCfg *dtls.Config, opts ...udp.DialOption) (*udpClient.ClientConn, error) {
	cfg := DefaultConfig
	for _, o := range opts {
		o.UDPClientApply(&cfg)
	}

	c, err := cfg.Dialer.DialContext(cfg.Ctx, cfg.Net, target)
	if err != nil {
		return nil, err
	}

	conn, err := dtls.Client(c, dtlsCfg)
	if err != nil {
		return nil, err
	}
	opts = append(opts, options.WithCloseSocket())
	return Client(conn, opts...), nil
}

// Client creates client over dtls connection.
func Client(conn *dtls.Conn, opts ...udp.DialOption) *udpClient.ClientConn {
	cfg := DefaultConfig
	for _, o := range opts {
		o.UDPClientApply(&cfg)
	}
	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}
	if cfg.CreateInactivityMonitor == nil {
		cfg.CreateInactivityMonitor = func() udpClient.InactivityMonitor {
			return inactivity.NewNilMonitor[*udpClient.ClientConn]()
		}
	}
	if cfg.MessagePool == nil {
		cfg.MessagePool = pool.New(0, 0)
	}
	errorsFunc := cfg.Errors
	cfg.Errors = func(err error) {
		if coapNet.IsCancelOrCloseError(err) {
			// this error was produced by cancellation context or closing connection.
			return
		}
		errorsFunc(fmt.Errorf("dtls: %v: %w", conn.RemoteAddr(), err))
	}

	createBlockWise := func(cc *udpClient.ClientConn) *blockwise.BlockWise[*udpClient.ClientConn] {
		return nil
	}
	if cfg.BlockwiseEnable {
		createBlockWise = func(cc *udpClient.ClientConn) *blockwise.BlockWise[*udpClient.ClientConn] {
			return blockwise.New(
				cc,
				cfg.BlockwiseTransferTimeout,
				cfg.Errors,
				false,
				cc.GetObservationRequest,
			)
		}
	}

	monitor := cfg.CreateInactivityMonitor()
	l := coapNet.NewConn(conn)
	session := server.NewSession(cfg.Ctx,
		l,
		cfg.MaxMessageSize,
		cfg.CloseSocket,
	)
	cc := udpClient.NewClientConn(session,
		createBlockWise,
		monitor,
		cache.NewCache(),
		&cfg,
	)

	cfg.PeriodicRunner(func(now time.Time) bool {
		cc.CheckExpirations(now)
		return cc.Context().Err() == nil
	})

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.Errors(err)
		}
	}()

	return cc
}
