package server

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/cache"
	coapSync "github.com/plgd-dev/go-coap/v2/pkg/sync"
	"github.com/plgd-dev/go-coap/v2/udp/client"
)

type Server struct {
	doneCtx           context.Context
	ctx               context.Context
	multicastRequests *client.RequestsMap
	multicastHandler  *coapSync.Map[uint64, HandlerFunc]
	serverStartedChan chan struct{}
	doneCancel        context.CancelFunc
	cancel            context.CancelFunc
	cache             *cache.Cache

	connsMutex sync.Mutex
	conns      map[string]*client.ClientConn

	listenMutex sync.Mutex
	listen      *coapNet.UDPConn

	cfg *Config
}

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type Option interface {
	UDPServerApply(cfg *Config)
}

func New(opt ...Option) *Server {
	cfg := DefaultConfig
	for _, o := range opt {
		o.UDPServerApply(&cfg)
	}

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
		cfg.CreateInactivityMonitor = func() client.InactivityMonitor {
			return inactivity.NewNilMonitor[*client.ClientConn]()
		}
	}
	if cfg.MessagePool == nil {
		cfg.MessagePool = pool.New(0, 0)
	}

	ctx, cancel := context.WithCancel(cfg.Ctx)
	serverStartedChan := make(chan struct{})

	doneCtx, doneCancel := context.WithCancel(context.Background())
	errorsFunc := cfg.Errors
	cfg.Errors = func(err error) {
		if coapNet.IsCancelOrCloseError(err) {
			// this error was produced by cancellation context or closing connection.
			return
		}
		errorsFunc(fmt.Errorf("udp: %w", err))
	}
	return &Server{
		ctx:               ctx,
		cancel:            cancel,
		multicastHandler:  coapSync.NewMap[uint64, HandlerFunc](),
		multicastRequests: coapSync.NewMap[uint64, *pool.Message](),
		serverStartedChan: serverStartedChan,
		doneCtx:           doneCtx,
		doneCancel:        doneCancel,
		cache:             cache.NewCache(),
		conns:             make(map[string]*client.ClientConn),

		cfg: &cfg,
	}
}

func (s *Server) checkAndSetListener(l *coapNet.UDPConn) error {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	if s.listen != nil {
		return fmt.Errorf("server already serve: %v", s.listen.LocalAddr().String())
	}
	s.listen = l
	close(s.serverStartedChan)
	return nil
}

func (s *Server) closeConnection(cc *client.ClientConn) {
	if err := cc.Close(); err != nil {
		s.cfg.Errors(fmt.Errorf("cannot close connection: %w", err))
	}
}

func (s *Server) Serve(l *coapNet.UDPConn) error {
	if s.cfg.BlockwiseSZX > blockwise.SZX1024 {
		return fmt.Errorf("invalid blockwiseSZX")
	}

	err := s.checkAndSetListener(l)
	if err != nil {
		return err
	}

	defer func() {
		s.closeSessions()
		s.doneCancel()
		s.listenMutex.Lock()
		defer s.listenMutex.Unlock()
		s.listen = nil
		s.serverStartedChan = make(chan struct{}, 1)
	}()

	m := make([]byte, s.cfg.MaxMessageSize)
	var wg sync.WaitGroup

	s.cfg.PeriodicRunner(func(now time.Time) bool {
		s.handleInactivityMonitors(now)
		s.cache.CheckExpirations(now)
		return s.ctx.Err() == nil
	})

	for {
		buf := m
		n, raddr, err := l.ReadWithContext(s.ctx, buf)
		if err != nil {
			wg.Wait()

			select {
			case <-s.ctx.Done():
				return nil
			default:
				if coapNet.IsCancelOrCloseError(err) {
					return nil
				}
				return err
			}
		}
		buf = buf[:n]
		cc, err := s.getClientConn(l, raddr, true)
		if err != nil {
			s.cfg.Errors(fmt.Errorf("%v: cannot get client connection: %w", raddr, err))
			continue
		}
		err = cc.Process(buf)
		if err != nil {
			s.closeConnection(cc)
			s.cfg.Errors(fmt.Errorf("%v: cannot process packet: %w", cc.RemoteAddr(), err))
		}
	}
}

func (s *Server) getListener() *coapNet.UDPConn {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	return s.listen
}

// Stop stops server without wait of ends Serve function.
func (s *Server) Stop() {
	s.cancel()
	l := s.getListener()
	if l != nil {
		if errC := l.Close(); errC != nil {
			s.cfg.Errors(fmt.Errorf("cannot close listener: %w", errC))
		}
	}
	s.closeSessions()
}

func (s *Server) closeSessions() {
	s.connsMutex.Lock()
	conns := s.conns
	s.conns = make(map[string]*client.ClientConn)
	s.connsMutex.Unlock()
	for _, cc := range conns {
		s.closeConnection(cc)
		if closeFn := getClose(cc); closeFn != nil {
			closeFn()
		}
	}
}

func (s *Server) conn() *coapNet.UDPConn {
	s.listenMutex.Lock()
	serverStartedChan := s.serverStartedChan
	s.listenMutex.Unlock()
	select {
	case <-serverStartedChan:
	case <-s.ctx.Done():
	}
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	return s.listen
}

const closeKey = "gocoapCloseConnection"

func (s *Server) getClientConns() []*client.ClientConn {
	s.connsMutex.Lock()
	defer s.connsMutex.Unlock()
	conns := make([]*client.ClientConn, 0, 32)
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	return conns
}

func (s *Server) handleInactivityMonitors(now time.Time) {
	for _, cc := range s.getClientConns() {
		select {
		case <-cc.Context().Done():
			if closeFn := getClose(cc); closeFn != nil {
				closeFn()
			}
			continue
		default:
			cc.CheckExpirations(now)
		}
	}
}

func getClose(cc *client.ClientConn) func() {
	v := cc.Context().Value(closeKey)
	if v == nil {
		return nil
	}
	return v.(func())
}

func bwCreateHandlerFunc(messagePool *pool.Pool, observatioRequests *client.RequestsMap) func(token message.Token) (*pool.Message, bool) {
	return func(token message.Token) (*pool.Message, bool) {
		var bwMessage *pool.Message
		_, ok := observatioRequests.LoadWithFunc(token.Hash(), func(m *pool.Message) *pool.Message {
			bwMessage = messagePool.AcquireMessage(m.Context())
			bwMessage.ResetOptionsTo(m.Options())
			bwMessage.SetCode(m.Code())
			bwMessage.SetToken(m.Token())
			bwMessage.SetMessageID(m.MessageID())
			return m
		})
		if !ok {
			return nil, false
		}
		return bwMessage, true
	}
}

func (s *Server) getOrCreateClientConn(UDPConn *coapNet.UDPConn, raddr *net.UDPAddr) (cc *client.ClientConn, created bool) {
	s.connsMutex.Lock()
	defer s.connsMutex.Unlock()
	key := raddr.String()
	cc = s.conns[key]
	if cc == nil {
		created = true
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
					bwCreateHandlerFunc(s.cfg.MessagePool, s.multicastRequests),
				)
			}
		}
		session := NewSession(
			s.ctx,
			UDPConn,
			raddr,
			s.cfg.MaxMessageSize,
			false,
			s.doneCtx,
		)
		monitor := s.cfg.CreateInactivityMonitor()
		cfg := client.DefaultConfig
		cfg.TransmissionNStart = s.cfg.TransmissionNStart
		cfg.TransmissionAcknowledgeTimeout = s.cfg.TransmissionAcknowledgeTimeout
		cfg.TransmissionMaxRetransmit = s.cfg.TransmissionMaxRetransmit
		cfg.Handler = func(w *responsewriter.ResponseWriter[*client.ClientConn], r *pool.Message) {
			h, ok := s.multicastHandler.Load(r.Token().Hash())
			if ok {
				h(w, r)
				return
			}
			s.cfg.Handler(w, r)
		}
		cfg.BlockwiseSZX = s.cfg.BlockwiseSZX
		cfg.GoPool = s.cfg.GoPool
		cfg.Errors = s.cfg.Errors
		cfg.GetMID = s.cfg.GetMID
		cfg.GetToken = s.cfg.GetToken
		cfg.MessagePool = s.cfg.MessagePool

		cc = client.NewClientConn(
			session,
			createBlockWise,
			monitor,
			s.cache,
			&cfg,
		)
		cc.SetContextValue(closeKey, func() {
			if err := session.Close(); err != nil {
				s.cfg.Errors(fmt.Errorf("cannot close session: %w", err))
			}
			session.shutdown()
		})
		cc.AddOnClose(func() {
			s.connsMutex.Lock()
			defer s.connsMutex.Unlock()
			if cc == s.conns[key] {
				delete(s.conns, key)
			}
		})
		s.conns[key] = cc
	}
	return cc, created
}

func (s *Server) getClientConn(l *coapNet.UDPConn, raddr *net.UDPAddr, firstTime bool) (*client.ClientConn, error) {
	cc, created := s.getOrCreateClientConn(l, raddr)
	if created {
		if s.cfg.OnNewClientConn != nil {
			s.cfg.OnNewClientConn(cc)
		}
	} else {
		// check if client is not expired now + 10ms  - if so, close it
		// 10ms - The expected maximum time taken by cc.CheckExpirations and cc.InactivityMonitor().Notify()
		cc.CheckExpirations(time.Now().Add(10 * time.Millisecond))
		if cc.Context().Err() == nil {
			// if client is not closed, extend expiration time
			cc.InactivityMonitor().Notify()
		}
	}

	if cc.Context().Err() != nil {
		// connection is closed so we need to create new one
		if closeFn := getClose(cc); closeFn != nil {
			closeFn()
		}
		if firstTime {
			return s.getClientConn(l, raddr, false)
		}
		return nil, fmt.Errorf("connection is closed")
	}
	return cc, nil
}

func (s *Server) NewClientConn(addr *net.UDPAddr) (*client.ClientConn, error) {
	l := s.getListener()
	if l == nil {
		// server is not started/stopped
		return nil, fmt.Errorf("server is not running")
	}
	return s.getClientConn(l, addr, true)
}
