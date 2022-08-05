package udp

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/server"
)

// A Option sets options such as credentials, keepalive parameters, etc.
type Option interface {
	UDPClientApply(cfg *client.Config)
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...Option) (*client.ClientConn, error) {
	cfg := client.DefaultConfig
	for _, o := range opts {
		o.UDPClientApply(&cfg)
	}
	c, err := cfg.Dialer.DialContext(cfg.Ctx, cfg.Net, target)
	if err != nil {
		return nil, err
	}
	conn, ok := c.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("unsupported connection type: %T", c)
	}
	opts = append(opts, options.WithCloseSocket())
	return Client(conn, opts...), nil
}

// Client creates client over udp connection.
func Client(conn *net.UDPConn, opts ...Option) *client.ClientConn {
	cfg := client.DefaultConfig
	for _, o := range opts {
		o.UDPClientApply(&cfg)
	}
	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}
	if cfg.CreateInactivityMonitor == nil {
		cfg.CreateInactivityMonitor = func() client.InactivityMonitor {
			return inactivity.NewNilMonitor[*client.ClientConn]()
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
		errorsFunc(fmt.Errorf("udp: %v: %w", conn.RemoteAddr(), err))
	}
	addr, _ := conn.RemoteAddr().(*net.UDPAddr)
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

	monitor := cfg.CreateInactivityMonitor()
	responseMsgCache := cache.NewCache[string, []byte]()
	l := coapNet.NewUDPConn(cfg.Net, conn, coapNet.WithErrors(cfg.Errors))
	session := server.NewSession(cfg.Ctx,
		l,
		addr,
		cfg.MaxMessageSize,
		cfg.CloseSocket,
		context.Background(),
	)
	cc := client.NewClientConn(session,
		createBlockWise,
		monitor,
		responseMsgCache,
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
