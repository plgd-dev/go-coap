package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/pkg/connections"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
)

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

type Server struct {
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *Config

	listenMutex sync.Mutex
	listen      Listener
}

// A Option sets options such as credentials, codec and keepalive parameters, etc.
type Option interface {
	DTLSServerApply(cfg *Config)
}

func New(opt ...Option) *Server {
	cfg := DefaultConfig
	for _, o := range opt {
		o.DTLSServerApply(&cfg)
	}

	ctx, cancel := context.WithCancel(cfg.Ctx)
	if cfg.Errors == nil {
		cfg.Errors = func(error) {
			// default no-op
		}
	}

	if cfg.GetMID == nil {
		cfg.GetMID = message.GetMID
	}

	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}

	if cfg.CreateInactivityMonitor == nil {
		cfg.CreateInactivityMonitor = func() udpClient.InactivityMonitor {
			return inactivity.NewNilMonitor[*udpClient.Conn]()
		}
	}

	if cfg.MessagePool == nil {
		cfg.MessagePool = pool.New(0, 0)
	}

	errorsFunc := cfg.Errors
	// assign updated func to cfg.errors so cfg.handler also uses the updated error handler
	cfg.Errors = func(err error) {
		if coapNet.IsCancelOrCloseError(err) {
			// this error was produced by cancellation context or closing connection.
			return
		}
		errorsFunc(fmt.Errorf("dtls: %w", err))
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
		return errors.New("server already serves listener")
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

func (s *Server) checkAcceptError(err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, coapNet.ErrListenerIsClosed):
		s.Stop()
		return false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		select {
		case <-s.ctx.Done():
		default:
			s.cfg.Errors(fmt.Errorf("cannot accept connection: %w", err))
			return true
		}
		return false
	default:
		return true
	}
}

func (s *Server) serveConnection(connections *connections.Connections, rw net.Conn) {
	inactivityMonitor := s.cfg.CreateInactivityMonitor()
	requestMonitor := s.cfg.RequestMonitor
	dtlsConn := coapNet.NewConn(rw)
	cc := s.createConn(dtlsConn, inactivityMonitor, requestMonitor)
	if s.cfg.OnNewConn != nil {
		s.cfg.OnNewConn(cc)
	}
	connections.Store(cc)
	defer connections.Delete(cc)

	if err := cc.Run(); err != nil {
		s.cfg.Errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
	}
}

func (s *Server) Serve(l Listener) error {
	if s.cfg.BlockwiseSZX > blockwise.SZX1024 {
		return errors.New("invalid blockwiseSZX")
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
		if ok := s.checkAcceptError(err); !ok {
			return nil
		}
		if err != nil || rw == nil {
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

func (s *Server) createConn(connection *coapNet.Conn, inactivityMonitor udpClient.InactivityMonitor, requestMonitor udpClient.RequestMonitorFunc) *udpClient.Conn {
	createBlockWise := func(*udpClient.Conn) *blockwise.BlockWise[*udpClient.Conn] {
		return nil
	}
	if s.cfg.BlockwiseEnable {
		createBlockWise = func(cc *udpClient.Conn) *blockwise.BlockWise[*udpClient.Conn] {
			v := cc
			return blockwise.New(
				v,
				s.cfg.BlockwiseTransferTimeout,
				s.cfg.Errors,
				func(token message.Token) (*pool.Message, bool) {
					return v.GetObservationRequest(token)
				},
			)
		}
	}
	session := NewSession(
		s.ctx,
		connection,
		s.cfg.MaxMessageSize,
		s.cfg.MTU,
		true,
	)
	cfg := udpClient.DefaultConfig
	cfg.TransmissionNStart = s.cfg.TransmissionNStart
	cfg.TransmissionAcknowledgeTimeout = s.cfg.TransmissionAcknowledgeTimeout
	cfg.TransmissionMaxRetransmit = s.cfg.TransmissionMaxRetransmit
	cfg.Handler = s.cfg.Handler
	cfg.BlockwiseSZX = s.cfg.BlockwiseSZX
	cfg.Errors = s.cfg.Errors
	cfg.GetMID = s.cfg.GetMID
	cfg.GetToken = s.cfg.GetToken
	cfg.MessagePool = s.cfg.MessagePool
	cfg.ReceivedMessageQueueSize = s.cfg.ReceivedMessageQueueSize
	cfg.ProcessReceivedMessage = s.cfg.ProcessReceivedMessage

	cc := udpClient.NewConnWithOpts(
		session,
		&cfg,
		udpClient.WithBlockWise(createBlockWise),
		udpClient.WithInactivityMonitor(inactivityMonitor),
		udpClient.WithRequestMonitor(requestMonitor),
	)

	return cc
}
