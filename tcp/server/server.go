package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/pkg/connections"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
)

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type ServerOption interface {
	TCPServerApply(cfg *Config)
}

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

type Server struct {
	listenMutex sync.Mutex
	listen      Listener
	ctx         context.Context
	cancel      context.CancelFunc
	cfg         *Config
}

func New(opt ...ServerOption) *Server {
	cfg := DefaultConfig
	for _, o := range opt {
		o.TCPServerApply(&cfg)
	}

	ctx, cancel := context.WithCancel(cfg.Ctx)

	if cfg.CreateInactivityMonitor == nil {
		cfg.CreateInactivityMonitor = func() client.InactivityMonitor {
			return inactivity.NewNilMonitor[*client.ClientConn]()
		}
	}
	if cfg.MessagePool == nil {
		cfg.MessagePool = pool.New(0, 0)
	}

	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}
	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}
	errorsFunc := cfg.Errors
	// assign updated func to opts.errors so opts.handler also uses the updated error handler
	cfg.Errors = func(err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
			// this error was produced by cancellation context or closing connection.
			return
		}
		errorsFunc(fmt.Errorf("tcp: %w", err))
	}

	return &Server{
		ctx:    ctx,
		cancel: cancel,
		cfg:    &cfg,
	}
}

func (s *Server) checkAndSetListener(l Listener) error {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	if s.listen != nil {
		return fmt.Errorf("server already serves listener")
	}
	s.listen = l
	return nil
}

func (s *Server) popListener() Listener {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	l := s.listen
	s.listen = nil
	return l
}

func (s *Server) checkAcceptError(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	switch {
	case errors.Is(err, coapNet.ErrListenerIsClosed):
		s.Stop()
		return false, nil
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		select {
		case <-s.ctx.Done():
		default:
			s.cfg.Errors(fmt.Errorf("cannot accept connection: %w", err))
			return true, nil
		}
		return false, nil
	default:
		return true, nil
	}
}

func (s *Server) serveConnection(connections *connections.Connections, rw net.Conn) {
	var cc *client.ClientConn
	monitor := s.cfg.CreateInactivityMonitor()
	cc = s.createClientConn(coapNet.NewConn(rw), monitor)
	if s.cfg.OnNewClientConn != nil {
		s.cfg.OnNewClientConn(cc)
	}
	connections.Store(cc)
	defer connections.Delete(cc)

	if err := cc.Run(); err != nil {
		s.cfg.Errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
	}
}

func (s *Server) Serve(l Listener) error {
	if s.cfg.BlockwiseSZX > blockwise.SZXBERT {
		return fmt.Errorf("invalid blockwiseSZX")
	}

	err := s.checkAndSetListener(l)
	if err != nil {
		return err
	}
	defer func() {
		s.Stop()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()

	connections := connections.New()
	s.cfg.PeriodicRunner(func(now time.Time) bool {
		connections.CheckExpirations(now)
		return s.ctx.Err() == nil
	})
	defer connections.Close()

	for {
		rw, err := l.AcceptWithContext(s.ctx)
		ok, err := s.checkAcceptError(err)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if rw == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveConnection(connections, rw)
		}()
	}
}

// Stop stops server without wait of ends Serve function.
func (s *Server) Stop() {
	s.cancel()
	l := s.popListener()
	if l == nil {
		return
	}
	if err := l.Close(); err != nil {
		s.cfg.Errors(fmt.Errorf("cannot close listener: %w", err))
	}
}

func (s *Server) createClientConn(connection *coapNet.Conn, monitor client.InactivityMonitor) *client.ClientConn {
	createBlockWise := func(cc *client.ClientConn) *blockwise.BlockWise[*client.ClientConn] {
		return nil
	}
	if s.cfg.BlockwiseEnable {
		createBlockWise = func(cc *client.ClientConn) *blockwise.BlockWise[*client.ClientConn] {
			return blockwise.New(
				cc,
				s.cfg.BlockwiseTransferTimeout,
				s.cfg.Errors,
				false,
				func(token message.Token) (*pool.Message, bool) {
					return nil, false
				},
			)
		}
	}
	cfg := client.DefaultConfig
	cfg.Ctx = s.ctx
	cfg.Handler = s.cfg.Handler
	cfg.MaxMessageSize = s.cfg.MaxMessageSize
	cfg.GoPool = s.cfg.GoPool
	cfg.Errors = s.cfg.Errors
	cfg.BlockwiseSZX = s.cfg.BlockwiseSZX
	cfg.DisablePeerTCPSignalMessageCSMs = s.cfg.DisablePeerTCPSignalMessageCSMs
	cfg.DisableTCPSignalMessageCSM = s.cfg.DisableTCPSignalMessageCSM
	cfg.CloseSocket = true
	cfg.ConnectionCacheSize = s.cfg.ConnectionCacheSize
	cfg.MessagePool = s.cfg.MessagePool
	cfg.GetToken = s.cfg.GetToken
	cc := client.NewClientConn(
		connection,
		createBlockWise,
		monitor,
		&cfg,
	)

	return cc
}
