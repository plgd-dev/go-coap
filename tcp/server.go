package tcp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/message"

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

type BlockwiseFactoryFunc = func(getSendedRequest func(token message.Token) (blockwise.Message, bool)) *blockwise.BlockWise

type OnNewClientConnFunc = func(cc *ClientConn)

var defaultServerOptions = serverOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	handler: func(w *ResponseWriter, r *Message) {
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
	keepalive:                keepalive.New(),
	blockwiseEnable:          true,
	blockwiseSZX:             blockwise.SZX1024,
	blockwiseTransferTimeout: time.Second * 3,
	onNewClientConn:          func(cc *ClientConn) {},
	heartBeat:                time.Millisecond * 100,
}

type serverOptions struct {
	ctx                             context.Context
	maxMessageSize                  int
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	keepalive                       *keepalive.KeepAlive
	blockwiseSZX                    blockwise.SZX
	blockwiseEnable                 bool
	blockwiseTransferTimeout        time.Duration
	onNewClientConn                 OnNewClientConnFunc
	heartBeat                       time.Duration
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool
}

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

type Server struct {
	maxMessageSize                  int
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	keepalive                       *keepalive.KeepAlive
	blockwiseSZX                    blockwise.SZX
	blockwiseEnable                 bool
	blockwiseTransferTimeout        time.Duration
	onNewClientConn                 OnNewClientConnFunc
	heartBeat                       time.Duration
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool

	conns             map[string]*ClientConn
	connsMutex        sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	serverStartedChan chan struct{}

	multicastRequests *sync.Map
	multicastHandler  *HandlerContainer
	msgID             uint32

	listen      Listener
	listenMutex sync.Mutex
}

func NewServer(opt ...ServerOption) *Server {
	opts := defaultServerOptions
	for _, o := range opt {
		o.apply(&opts)
	}

	ctx, cancel := context.WithCancel(opts.ctx)
	serverStartedChan := make(chan struct{})

	return &Server{
		ctx:                      ctx,
		cancel:                   cancel,
		handler:                  opts.handler,
		maxMessageSize:           opts.maxMessageSize,
		errors:                   opts.errors,
		goPool:                   opts.goPool,
		keepalive:                opts.keepalive,
		blockwiseSZX:             opts.blockwiseSZX,
		blockwiseEnable:          opts.blockwiseEnable,
		blockwiseTransferTimeout: opts.blockwiseTransferTimeout,
		serverStartedChan:        serverStartedChan,
	}
}

func (s *Server) Serve(l Listener) error {
	if s.blockwiseSZX > blockwise.SZXBERT {
		return fmt.Errorf("invalid blockwiseSZX")
	}

	s.listenMutex.Lock()
	if s.listen != nil {
		s.listenMutex.Unlock()
		return fmt.Errorf("server already serve listener")
	}
	s.listen = l
	close(s.serverStartedChan)
	s.listenMutex.Unlock()
	defer func() {
		s.listenMutex.Lock()
		defer s.listenMutex.Unlock()
		s.listen = nil
		s.serverStartedChan = make(chan struct{}, 1)
	}()

	var wg sync.WaitGroup
	for {
		rw, err := l.AcceptWithContext(s.ctx)
		if err != nil {
			switch err {
			case context.DeadlineExceeded, context.Canceled:
				wg.Wait()
				return fmt.Errorf("cannot accept: %w", err)
			default:
				continue
			}
		}
		if rw != nil {
			wg.Add(1)
			cc := s.createClientConn(coapNet.NewConn(rw, coapNet.WithHeartBeat(s.heartBeat)))
			if s.onNewClientConn != nil {
				s.onNewClientConn(cc)
			}
			go func() {
				defer wg.Done()
				err := cc.Run()
				if err != nil {
					s.errors(err)
				}
			}()
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
	}
}

// Stop stops server without wait of ends Serve function.
func (s *Server) Stop() {
	s.cancel()
}

func (s *Server) createClientConn(connection *coapNet.Conn) *ClientConn {
	var blockWise *blockwise.BlockWise
	if s.blockwiseEnable {
		blockWise = blockwise.NewBlockWise(func(ctx context.Context) blockwise.Message {
			return AcquireMessage(ctx)
		}, func(m blockwise.Message) {
			ReleaseMessage(m.(*Message))
		}, s.blockwiseTransferTimeout, s.errors, false, func(token message.Token) (blockwise.Message, bool) {
			msg, ok := s.multicastRequests.Load(token.String())
			if !ok {
				return nil, ok
			}
			return msg.(blockwise.Message), ok
		})
	}
	obsHandler := NewHandlerContainer()
	cc := NewClientConn(
		NewSession(
			s.ctx,
			connection,
			NewObservatiomHandler(obsHandler, func(w *ResponseWriter, r *Message) {
				s.handler(w, r)
			}),
			s.maxMessageSize, s.goPool, s.blockwiseSZX, blockWise, s.disablePeerTCPSignalMessageCSMs, s.disableTCPSignalMessageCSM),
		obsHandler, nil,
	)

	return cc
}
