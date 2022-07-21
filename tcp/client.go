package tcp

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/pkg/options"
	client "github.com/plgd-dev/go-coap/v2/tcp/client"
)

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	TCPClientApply(cfg *client.Config)
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...DialOption) (*client.ClientConn, error) {
	cfg := client.DefaultConfig
	for _, o := range opts {
		o.TCPClientApply(&cfg)
	}

	var conn net.Conn
	var err error
	if cfg.TLSCfg != nil {
		conn, err = tls.DialWithDialer(cfg.Dialer, cfg.Net, target, cfg.TLSCfg)
	} else {
		conn, err = cfg.Dialer.DialContext(cfg.Ctx, cfg.Net, target)
	}
	if err != nil {
		return nil, err
	}
	opts = append(opts, options.WithCloseSocket())
	return Client(conn, opts...), nil
}

// Client creates client over tcp/tcp-tls connection.
func Client(conn net.Conn, opts ...DialOption) *client.ClientConn {
	cfg := client.DefaultConfig
	for _, o := range opts {
		o.TCPClientApply(&cfg)
	}
	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}
	if cfg.CreateInactivityMonitor == nil {
		cfg.CreateInactivityMonitor = func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
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
		errorsFunc(fmt.Errorf("tcp: %w", err))
	}

	createBlockWise := func(cc *client.ClientConn) *blockwise.BlockWise[*client.ClientConn] {
		return nil
	}
	if cfg.BlockwiseEnable {
		createBlockWise = func(cc *client.ClientConn) *blockwise.BlockWise[*client.ClientConn] {
			return blockwise.New(
				cc,
				cfg.BlockwiseTransferTimeout,
				cfg.Errors,
				false,
				cc.GetObservationRequest,
			)
		}
	}

	l := coapNet.NewConn(conn)
	monitor := cfg.CreateInactivityMonitor()
	cc := client.NewClientConn(l,
		createBlockWise,
		monitor,
		&cfg,
	)

	cfg.PeriodicRunner(func(now time.Time) bool {
		cc.CheckExpirations(now)
		return cc.Context().Err() == nil
	})

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.Errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
		}
	}()

	return cc
}
