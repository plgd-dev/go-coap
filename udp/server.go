package udp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/keepalive"

	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"

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
type HandlerFunc func(*ResponseWriter, *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func() error) error

type BlockwiseFactoryFunc = func(getSendedRequest func(token message.Token) (blockwise.Message, bool)) *blockwise.BlockWise

type OnNewClientConnFunc = func(cc *ClientConn)

var defaultServerOptions = serverOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	handler: func(w *ResponseWriter, r *pool.Message) {
		w.SetResponse(codes.NotFound, message.TextPlain, nil)
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
	keepalive:                      keepalive.New(),
	blockwiseEnable:                true,
	blockwiseSZX:                   blockwise.SZX1024,
	blockwiseTransferTimeout:       time.Second * 3,
	onNewClientConn:                func(cc *ClientConn) {},
	transmissionNStart:             time.Second,
	transmissionAcknowledgeTimeout: time.Second * 2,
	transmissionMaxRetransmit:      4,
}

type serverOptions struct {
	ctx                            context.Context
	maxMessageSize                 int
	handler                        HandlerFunc
	errors                         ErrorFunc
	goPool                         GoPoolFunc
	keepalive                      *keepalive.KeepAlive
	net                            string
	blockwiseSZX                   blockwise.SZX
	blockwiseEnable                bool
	blockwiseTransferTimeout       time.Duration
	onNewClientConn                OnNewClientConnFunc
	transmissionNStart             time.Duration
	transmissionAcknowledgeTimeout time.Duration
	transmissionMaxRetransmit      int
}

type Server struct {
	maxMessageSize                 int
	handler                        HandlerFunc
	errors                         ErrorFunc
	goPool                         GoPoolFunc
	keepalive                      *keepalive.KeepAlive
	blockwiseSZX                   blockwise.SZX
	blockwiseEnable                bool
	blockwiseTransferTimeout       time.Duration
	onNewClientConn                OnNewClientConnFunc
	transmissionNStart             time.Duration
	transmissionAcknowledgeTimeout time.Duration
	transmissionMaxRetransmit      int

	conns             map[string]*ClientConn
	connsMutex        sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	serverStartedChan chan struct{}

	multicastRequests *sync.Map
	multicastHandler  *HandlerContainer
	msgID             uint32

	listen      *coapNet.UDPConn
	listenMutex sync.Mutex
}

func NewServer(opt ...ServerOption) *Server {
	opts := defaultServerOptions
	for _, o := range opt {
		o.apply(&opts)
	}

	ctx, cancel := context.WithCancel(opts.ctx)
	b := make([]byte, 4)
	rand.Read(b)
	msgID := binary.BigEndian.Uint32(b)
	serverStartedChan := make(chan struct{})

	return &Server{
		ctx:                            ctx,
		cancel:                         cancel,
		handler:                        opts.handler,
		maxMessageSize:                 opts.maxMessageSize,
		errors:                         opts.errors,
		goPool:                         opts.goPool,
		keepalive:                      opts.keepalive,
		blockwiseSZX:                   opts.blockwiseSZX,
		blockwiseEnable:                opts.blockwiseEnable,
		blockwiseTransferTimeout:       opts.blockwiseTransferTimeout,
		multicastHandler:               NewHandlerContainer(),
		multicastRequests:              &sync.Map{},
		msgID:                          msgID,
		serverStartedChan:              serverStartedChan,
		onNewClientConn:                opts.onNewClientConn,
		transmissionNStart:             opts.transmissionNStart,
		transmissionAcknowledgeTimeout: opts.transmissionAcknowledgeTimeout,
		transmissionMaxRetransmit:      opts.transmissionMaxRetransmit,

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
	close(s.serverStartedChan)
	s.listenMutex.Unlock()
	defer func() {
		s.closeSessions()
		s.listenMutex.Lock()
		defer s.listenMutex.Unlock()
		s.listen = nil
		s.serverStartedChan = make(chan struct{}, 1)
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
			if s.onNewClientConn != nil {
				s.onNewClientConn(cc)
			}
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
		err = cc.session.processBuffer(buf, cc)
		if err != nil {
			cc.Close()
			s.errors(err)
		}
	}
}

// Stop stops server without wait of ends Serve function.
func (s *Server) Stop() {
	s.cancel()
	s.closeSessions()
}

func (s *Server) closeSessions() {
	s.connsMutex.Lock()
	tmp := s.conns
	s.conns = make(map[string]*ClientConn)
	s.connsMutex.Unlock()
	for _, v := range tmp {
		v.Close()
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

func (s *Server) getOrCreateClientConn(UDPConn *coapNet.UDPConn, raddr *net.UDPAddr) (*ClientConn, bool) {
	s.connsMutex.Lock()
	defer s.connsMutex.Unlock()
	key := raddr.String()
	cc := s.conns[key]
	created := false
	if cc == nil {
		created = true
		var blockWise *blockwise.BlockWise
		if s.blockwiseEnable {
			blockWise = blockwise.NewBlockWise(func(ctx context.Context) blockwise.Message {
				return pool.AcquireMessage(ctx)
			}, func(m blockwise.Message) {
				pool.ReleaseMessage(m.(*pool.Message))
			}, s.blockwiseTransferTimeout, s.errors, false, func(token message.Token) (blockwise.Message, bool) {
				msg, ok := s.multicastRequests.Load(token.String())
				if !ok {
					return nil, ok
				}
				return msg.(blockwise.Message), ok
			})
		}
		obsHandler := NewHandlerContainer()
		cc = NewClientConn(
			NewSession(
				s.ctx,
				UDPConn,
				raddr,
				NewObservatiomHandler(obsHandler, func(w *ResponseWriter, r *pool.Message) {
					h, err := s.multicastHandler.Get(r.Token())
					if err == nil {
						h(w, r)
						return
					}
					s.handler(w, r)
				}),
				s.maxMessageSize, s.goPool, s.blockwiseSZX, blockWise),
			obsHandler, s.multicastRequests, s.transmissionNStart, s.transmissionAcknowledgeTimeout, s.transmissionMaxRetransmit,
		)
		cc.AddOnClose(func() {
			s.connsMutex.Lock()
			defer s.connsMutex.Unlock()
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

// Discover sends GET to multicast address and wait for responses until context timeouts or server shutdown.
func (s *Server) Discover(ctx context.Context, multicastAddr, path string, receiverFunc func(cc *ClientConn, resp *pool.Message), opts ...MulticastOption) error {
	req, err := NewGetRequest(ctx, path)
	if err != nil {
		return fmt.Errorf("cannot create discover request: %w", err)
	}
	req.SetMessageID(s.getMID())
	defer pool.ReleaseMessage(req)
	return s.DiscoveryRequest(req, multicastAddr, receiverFunc, opts...)
}

// DiscoveryRequest sends request to multicast addressand wait for responses until request timeouts or server shutdown.
func (s *Server) DiscoveryRequest(req *pool.Message, multicastAddr string, receiverFunc func(cc *ClientConn, resp *pool.Message), opts ...MulticastOption) error {
	token := req.Token()
	if len(token) == 0 {
		return fmt.Errorf("invalid token")
	}
	cfg := defaultMulticastOptions
	for _, o := range opts {
		o.apply(&cfg)
	}
	c := s.conn()
	if c == nil {
		return fmt.Errorf("server doesn't serve connection")
	}
	addr, err := net.ResolveUDPAddr(c.Network(), multicastAddr)
	if err != nil {
		return fmt.Errorf("cannot resolve address: %w", err)
	}
	if !addr.IP.IsMulticast() {
		return fmt.Errorf("invalid multicast address")
	}
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal req: %w", err)
	}
	s.multicastRequests.Store(token.String(), req)
	defer s.multicastRequests.Delete(token.String())
	err = s.multicastHandler.Insert(token, func(w *ResponseWriter, r *pool.Message) {
		receiverFunc(w.ClientConn(), r)
	})
	if err != nil {
		return err
	}
	defer s.multicastHandler.Pop(token)

	err = c.WriteMulticast(req.Context(), addr, cfg.hopLimit, data)
	if err != nil {
		return err
	}
	select {
	case <-req.Context().Done():
		return nil
	case <-s.ctx.Done():
		return fmt.Errorf("server was closed: %w", req.Context().Err())
	}
}

// GetMID generates a message id for UDP-coap
func (s *Server) getMID() uint16 {
	return uint16(atomic.AddUint32(&s.msgID, 1) % 0xffff)
}
