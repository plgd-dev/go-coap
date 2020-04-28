package udp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"

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
type HandlerFunc func(*ResponseWriter, *Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func() error) error

type BlockwiseFactoryFunc = func() *blockwise.BlockWise

var defaultServerOptions = serverOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	heartBeat:      time.Millisecond * 100,
	handler: func(w *ResponseWriter, r *Message) {
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
	keepalive:    keepalive.New(),
	net:          "udp",
	blockwiseSZX: blockwise.SZX1024,
	blockWiseFactory: func() *blockwise.BlockWise {
		return blockwise.NewBlockWise(func(ctx context.Context) blockwise.Message {
			return AcquireRequest(ctx)
		}, func(m blockwise.Message) {
			ReleaseRequest(m.(*Message))
		}, time.Second*3, func(err error) {
			fmt.Println(err)
		}, false)
	},
}

type serverOptions struct {
	ctx              context.Context
	maxMessageSize   int
	heartBeat        time.Duration
	handler          HandlerFunc
	errors           ErrorFunc
	goPool           GoPoolFunc
	keepalive        *keepalive.KeepAlive
	net              string
	blockwiseSZX     blockwise.SZX
	blockWiseFactory BlockwiseFactoryFunc
}

type Server struct {
	maxMessageSize   int
	heartBeat        time.Duration
	handler          HandlerFunc
	errors           ErrorFunc
	goPool           GoPoolFunc
	keepalive        *keepalive.KeepAlive
	blockwiseSZX     blockwise.SZX
	blockWiseFactory BlockwiseFactoryFunc
	net              string

	conns         map[string]*ClientConn
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
		handler = func(w *ResponseWriter, r *Message) {
			w.SetCode(codes.BadRequest)
		}
	}

	return &Server{
		ctx:              ctx,
		cancel:           cancel,
		handler:          handler,
		maxMessageSize:   opts.maxMessageSize,
		heartBeat:        opts.heartBeat,
		errors:           opts.errors,
		goPool:           opts.goPool,
		net:              opts.net,
		keepalive:        opts.keepalive,
		blockwiseSZX:     opts.blockwiseSZX,
		blockWiseFactory: opts.blockWiseFactory,

		conns: make(map[string]*ClientConn),
	}
}

func (s *Server) Serve(l *coapNet.UDPConn) error {
	if s.blockwiseSZX > blockwise.SZX1024 {
		return fmt.Errorf("invalid blockwiseSZX")
	}

	m := make([]byte, s.maxMessageSize)
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
		buf := m
		n, raddr, err := l.ReadWithContext(s.ctx, buf)
		if err != nil {
			wg.Wait()
			return err
		}
		buf = buf[:n]
		cc, created := s.getOrCreateClientConn(l, raddr)
		if created {
			if s.keepalive != nil {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := s.keepalive.Run(cc)
					if err != nil {
						s.errors(err)
					}
				}()
			}
		}
		err = cc.processBuffer(buf)
		if err != nil {
			cc.Close()
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
	tmp := s.conns
	s.conns = make(map[string]*ClientConn)
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

func (s *Server) getOrCreateClientConn(UDPConn *coapNet.UDPConn, raddr *net.UDPAddr) (*ClientConn, bool) {
	s.sessionsMutex.Lock()
	defer s.sessionsMutex.Unlock()
	key := raddr.String()
	cc := s.conns[key]
	created := false
	if cc == nil {
		created = true
		var blockWise *blockwise.BlockWise
		if s.blockWiseFactory != nil {
			blockWise = s.blockWiseFactory()
		}
		obsHandler := NewHandlerContainer()
		cc = NewClientConn(
			NewSession(s.ctx, UDPConn, raddr, NewObservatiomHandler(obsHandler, s.handler), s.maxMessageSize, s.goPool, s.blockwiseSZX, blockWise),
			obsHandler,
		)
		cc.AddOnClose(func() {
			s.sessionsMutex.Lock()
			defer s.sessionsMutex.Unlock()
			delete(s.conns, key)
		})
		s.conns[key] = cc
	}
	return cc, created
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

func (s *Server) WriteMulticastReq(req *Message, target string, opts ...MulticastOption) error {
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
