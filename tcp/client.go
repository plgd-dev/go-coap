package tcp

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/options"
	client "github.com/plgd-dev/go-coap/v3/tcp/client"
)

// A Option sets options such as credentials, keepalive parameters, etc.
type Option interface {
	TCPClientApply(cfg *client.Config)
}

func createBlockWiseFactory(cfg *client.Config) func(*client.Conn) *blockwise.BlockWise[*client.Conn] {
	if !cfg.BlockwiseEnable {
		return func(*client.Conn) *blockwise.BlockWise[*client.Conn] {
			return nil
		}
	}

	return func(cc *client.Conn) *blockwise.BlockWise[*client.Conn] {
		v := cc
		return blockwise.New(
			v,
			cfg.BlockwiseTransferTimeout,
			cfg.Errors,
			func(token message.Token) (*pool.Message, bool) {
				return v.GetObservationRequest(token)
			},
		)
	}
}

func setupCSMExchangeHandler(cfg *client.Config, cc *client.Conn) chan struct{} {
	if cfg.CSMExchangeTimeout == 0 || cfg.DisablePeerTCPSignalMessageCSMs {
		return nil
	}

	csmExchangeDone := make(chan struct{})
	cc.SetTCPSignalReceivedHandler(func(code codes.Code) {
		if code == codes.CSM {
			close(csmExchangeDone)
		}
	})
	return csmExchangeDone
}

func waitForCSMExchange(cfg *client.Config, cc *client.Conn, csmExchangeDone chan struct{}) error {
	if csmExchangeDone == nil {
		return nil
	}

	defer cc.SetTCPSignalReceivedHandler(nil)

	select {
	case <-time.After(cfg.CSMExchangeTimeout):
		err := fmt.Errorf("%v: timeout waiting for CSM exchange with peer", cc.RemoteAddr())
		cfg.Errors(err)
		cc.Close()
		return err
	case <-csmExchangeDone:
		return nil
	}
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...Option) (*client.Conn, error) {
	cfg := client.DefaultConfig
	for _, o := range opts {
		o.TCPClientApply(&cfg)
	}

	var conn net.Conn
	var err error
	if cfg.TLSCfg != nil {
		d := &tls.Dialer{
			NetDialer: cfg.Dialer,
			Config:    cfg.TLSCfg,
		}
		conn, err = d.DialContext(cfg.Ctx, cfg.Net, target)
	} else {
		conn, err = cfg.Dialer.DialContext(cfg.Ctx, cfg.Net, target)
	}
	if err != nil {
		return nil, err
	}
	opts = append(opts, options.WithCloseSocket())
	return Client(conn, opts...)
}

// Client creates client over tcp/tcp-tls connection.
func Client(conn net.Conn, opts ...Option) (*client.Conn, error) {
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
		cfg.CreateInactivityMonitor = func() client.InactivityMonitor {
			return inactivity.NewNilMonitor[*client.Conn]()
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

	createBlockWise := createBlockWiseFactory(&cfg)

	l := coapNet.NewConn(conn)
	monitor := cfg.CreateInactivityMonitor()
	cc := client.NewConnWithOpts(l,
		&cfg,
		client.WithBlockWise(createBlockWise),
		client.WithInactivityMonitor(monitor),
		client.WithRequestMonitor(cfg.RequestMonitor),
	)

	cfg.PeriodicRunner(func(now time.Time) bool {
		cc.CheckExpirations(now)
		return cc.Context().Err() == nil
	})

	csmExchangeDone := setupCSMExchangeHandler(&cfg, cc)

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.Errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
		}
	}()

	if err := waitForCSMExchange(&cfg, cc, csmExchangeDone); err != nil {
		return nil, err
	}

	return cc, nil
}
