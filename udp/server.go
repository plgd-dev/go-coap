package udp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-ocf/go-coap/v2/keepalive"

	"github.com/go-ocf/go-coap/v2/message/codes"

	coapNet "github.com/go-ocf/go-coap/v2/net"
)

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type ServerOption interface {
	apply(*serverOptions)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.  If f is a function
// with the appropriate signature, HandlerFunc(f) is a
// Handler object that calls f.
type HandlerFunc func(*ResponseWriter, *Request)

type ErrorFunc = func(error)

type GoPoolFunc = func(func() error) error

var defaultServerOptions = serverOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	heartBeat:      time.Millisecond * 100,
	handler: func(w *ResponseWriter, r *Request) {
		w.SetCode(codes.NotFound)
	},
	errors: func(err error) {
		fmt.Println(err)
	},
	goPool: func(f func() error) error {
		go func() {
			err := f()
			if err != nil {
				fmt.Println(err)
			}
		}()
		return nil
	},
	keepalive: keepalive.New(),
	net:       "udp",
}

type serverOptions struct {
	ctx            context.Context
	maxMessageSize int
	heartBeat      time.Duration
	handler        HandlerFunc
	errors         ErrorFunc
	goPool         GoPoolFunc
	keepalive      *keepalive.KeepAlive
	net            string
}

type Server struct {
	maxMessageSize int
	heartBeat      time.Duration
	handler        HandlerFunc
	errors         ErrorFunc
	goPool         GoPoolFunc
	keepalive      *keepalive.KeepAlive
	net            string

	sessions      map[string]*Session
	sessionsMutex sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc

	listen      *coapNet.UDPConn
	listenMutex sync.Mutex
}

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

func NewServer(handler HandlerFunc, opt ...ServerOption) *Server {
	opts := defaultServerOptions
	for _, o := range opt {
		o.apply(&opts)
	}

	ctx, cancel := context.WithCancel(opts.ctx)
	if handler == nil {
		handler = func(w *ResponseWriter, r *Request) {
			w.SetCode(codes.BadRequest)
		}
	}

	return &Server{
		ctx:            ctx,
		cancel:         cancel,
		handler:        handler,
		maxMessageSize: opts.maxMessageSize,
		heartBeat:      opts.heartBeat,
		errors:         opts.errors,
		goPool:         opts.goPool,
		net:            opts.net,
		keepalive:      opts.keepalive,

		sessions: make(map[string]*Session),
	}
}

func (s *Server) Serve(l *coapNet.UDPConn) error {
	m := make([]byte, ^uint16(0))
	s.listenMutex.Lock()
	if s.listen != nil {
		s.listenMutex.Unlock()
		return fmt.Errorf("server already serve: %v", s.listen.LocalAddr().String())
	}
	s.listen = l
	s.listenMutex.Unlock()
	defer func() {
		s.closeSessions()
		s.listenMutex.Lock()
		defer s.listenMutex.Unlock()
		s.listen = nil
	}()

	var wg sync.WaitGroup
	for {
		n, raddr, err := l.ReadWithContext(s.ctx, m)
		if err != nil {
			wg.Wait()
			return err
		}
		m = m[:n]

		session, created := s.getOrCreateSession(l, raddr)
		if created {
			if s.keepalive != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					conn := NewClientConn(session)
					err := s.keepalive.Run(conn)
					if err != nil {
						s.errors(err)
					}
				}()
			}
		}
		err = session.processBuffer(m)
		if err != nil {
			session.Close()
			s.errors(err)
		}
	}
}

func (s *Server) Stop() {
	s.cancel()
	s.closeSessions()
}

func (s *Server) closeSessions() {
	s.sessionsMutex.Lock()
	tmp := s.sessions
	s.sessions = make(map[string]*Session)
	s.sessionsMutex.Unlock()
	for _, v := range tmp {
		v.Close()
	}
}

func (s *Server) conn() *coapNet.UDPConn {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	return s.listen
}

func (s *Server) getOrCreateSession(UDPConn *coapNet.UDPConn, raddr *net.UDPAddr) (*Session, bool) {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()
	key := raddr.String()
	session := s.sessions[key]
	created := false
	if session == nil {
		created = true
		session = NewSession(s.ctx, UDPConn, raddr, s.handler, s.maxMessageSize, s.goPool)
		session.AddOnClose(func() {
			s.sessionsMutex.Lock()
			defer s.sessionsMutex.Unlock()
			delete(s.sessions, key)
		})
		s.sessions[key] = session
	}
	return session, created
}

var defaultMulticastOptions = multicastOptions{
	hopLimit: 2,
}

type multicastOptions struct {
	hopLimit int
}

// A MulticastOption sets options such as hop limit, etc.
type MulticastOption interface {
	apply(*multicastOptions)
}

func (s *Server) WriteMulticastReq(req *Request, target string, opts ...MulticastOption) error {
	cfg := defaultMulticastOptions
	for _, o := range opts {
		o.apply(&cfg)
	}

	addr, err := net.ResolveUDPAddr(s.net, target)
	if err != nil {
		return fmt.Errorf("cannot resolve address: %w", err)
	}
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal req: %w", err)
	}
	c := s.conn()
	if c == nil {
		return fmt.Errorf("server doesn't serve connection")
	}
	err = c.WriteMulticast(req.Context(), addr, cfg.hopLimit, data)
	return err
}
