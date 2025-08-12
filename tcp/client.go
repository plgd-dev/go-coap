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

	createBlockWise := func(*client.Conn) *blockwise.BlockWise[*client.Conn] {
		return nil
	}
	if cfg.BlockwiseEnable {
		createBlockWise = func(cc *client.Conn) *blockwise.BlockWise[*client.Conn] {
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

	var csmExchangeDone chan struct{}
	if cfg.CSMExchangeTimeout != 0 && !cfg.DisablePeerTCPSignalMessageCSMs {
		csmExchangeDone = make(chan struct{})

		cc.SetTCPSignalReceivedHandler(func(code codes.Code) {
			if code == codes.CSM {
				close(csmExchangeDone)
			}
		})
	}

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.Errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
		}
	}()

	// if CSM messages are enabled, wait for the CSM messages to be exchanged
	if cfg.CSMExchangeTimeout != 0 && !cfg.DisablePeerTCPSignalMessageCSMs {
		select {
		case <-time.After(cfg.CSMExchangeTimeout):
			err := fmt.Errorf("%v: timeout waiting for CSM exchange with peer", cc.RemoteAddr())
			cfg.Errors(err)
			cc.Close()      // Close connection on timeout
			return nil, err // or return cc with an error state
		case <-csmExchangeDone:
			// CSM exchange completed successfully
		}
		// Clear the handler after exchange is complete or timed out
		cc.SetTCPSignalReceivedHandler(nil)
	}

	return cc, nil
}
