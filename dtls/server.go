package dtls

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	udpMessage "github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	kitSync "github.com/plgd-dev/kit/v2/sync"
)

// A ServerOption sets options such as credentials, codec and keepalive parameters, etc.
type ServerOption interface {
	apply(*serverOptions)
}

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*client.ResponseWriter, *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

type BlockwiseFactoryFunc = func(getSendedRequest func(token message.Token) (blockwise.Message, bool)) *blockwise.BlockWise

// OnNewClientConnFunc is the callback for new connections.
//
// Note: Calling `dtlsConn.Close()` is forbidden, and `dtlsConn` should be treated as a
// "read-only" parameter, mainly used to get the peer certificate from the underlining connection
type OnNewClientConnFunc = func(cc *client.ClientConn, dtlsConn *dtls.Conn)

type GetMIDFunc = func() uint16

var defaultServerOptions = serverOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	handler: func(w *client.ResponseWriter, r *pool.Message) {
		w.SetResponse(codes.NotFound, message.TextPlain, nil)
	},
	errors: func(err error) {
		fmt.Println(err)
	},
	goPool: func(f func()) error {
		go func() {
			f()
		}()
		return nil
	},
	createInactivityMonitor: func() inactivity.Monitor {
		return inactivity.NewNilMonitor()
	},
	blockwiseEnable:                true,
	blockwiseSZX:                   blockwise.SZX1024,
	blockwiseTransferTimeout:       time.Second * 5,
	onNewClientConn:                func(cc *client.ClientConn, dtlsConn *dtls.Conn) {},
	heartBeat:                      time.Millisecond * 100,
	transmissionNStart:             time.Second,
	transmissionAcknowledgeTimeout: time.Second * 2,
	transmissionMaxRetransmit:      4,
	getMID:                         udpMessage.GetMID,
}

type serverOptions struct {
	ctx                            context.Context
	maxMessageSize                 int
	handler                        HandlerFunc
	errors                         ErrorFunc
	goPool                         GoPoolFunc
	createInactivityMonitor        func() inactivity.Monitor
	blockwiseSZX                   blockwise.SZX
	blockwiseEnable                bool
	blockwiseTransferTimeout       time.Duration
	onNewClientConn                OnNewClientConnFunc
	heartBeat                      time.Duration
	transmissionNStart             time.Duration
	transmissionAcknowledgeTimeout time.Duration
	transmissionMaxRetransmit      int
	getMID                         GetMIDFunc
}

// Listener defined used by coap
type Listener interface {
	Close() error
	AcceptWithContext(ctx context.Context) (net.Conn, error)
}

type Server struct {
	maxMessageSize                 int
	handler                        HandlerFunc
	errors                         ErrorFunc
	goPool                         GoPoolFunc
	createInactivityMonitor        func() inactivity.Monitor
	blockwiseSZX                   blockwise.SZX
	blockwiseEnable                bool
	blockwiseTransferTimeout       time.Duration
	onNewClientConn                OnNewClientConnFunc
	heartBeat                      time.Duration
	transmissionNStart             time.Duration
	transmissionAcknowledgeTimeout time.Duration
	transmissionMaxRetransmit      int
	getMID                         GetMIDFunc

	ctx    context.Context
	cancel context.CancelFunc

	listen      Listener
	listenMutex sync.Mutex
}

func NewServer(opt ...ServerOption) *Server {
	opts := defaultServerOptions
	for _, o := range opt {
		o.apply(&opts)
	}

	ctx, cancel := context.WithCancel(opts.ctx)
	if opts.errors == nil {
		opts.errors = func(error) {}
	}

	if opts.getMID == nil {
		opts.getMID = udpMessage.GetMID
	}

	if opts.createInactivityMonitor == nil {
		opts.createInactivityMonitor = func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		}
	}

	return &Server{
		ctx:            ctx,
		cancel:         cancel,
		handler:        opts.handler,
		maxMessageSize: opts.maxMessageSize,
		errors: func(err error) {
			if errors.Is(err, context.Canceled) {
				// this error was produced by cancellation context - don't report it.
				return
			}
			opts.errors(fmt.Errorf("dtls: %w", err))
		},
		goPool:                         opts.goPool,
		createInactivityMonitor:        opts.createInactivityMonitor,
		blockwiseSZX:                   opts.blockwiseSZX,
		blockwiseEnable:                opts.blockwiseEnable,
		blockwiseTransferTimeout:       opts.blockwiseTransferTimeout,
		onNewClientConn:                opts.onNewClientConn,
		heartBeat:                      opts.heartBeat,
		transmissionNStart:             opts.transmissionNStart,
		transmissionAcknowledgeTimeout: opts.transmissionAcknowledgeTimeout,
		transmissionMaxRetransmit:      opts.transmissionMaxRetransmit,
		getMID:                         opts.getMID,
	}
}

func (s *Server) checkAndSetListener(l Listener) error {
	s.listenMutex.Lock()
	defer s.listenMutex.Unlock()
	if s.listen != nil {
		return fmt.Errorf("server already serve listener")
	}
	s.listen = l
	return nil
}

func (s *Server) checkAcceptError(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	switch err {
	case coapNet.ErrListenerIsClosed:
		s.Stop()
		return false, nil
	case context.DeadlineExceeded, context.Canceled:
		select {
		case <-s.ctx.Done():
		default:
			s.errors(fmt.Errorf("cannot accept connection: %w", err))
			return true, nil
		}
		return false, nil
	default:
		return true, nil
	}
}

func (s *Server) Serve(l Listener) error {
	if s.blockwiseSZX > blockwise.SZX1024 {
		return fmt.Errorf("invalid blockwiseSZX")
	}
	err := s.checkAndSetListener(l)
	if err != nil {
		return err
	}
	defer func() {
		s.listenMutex.Lock()
		defer s.listenMutex.Unlock()
		s.listen = nil
	}()

	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		rw, err := l.AcceptWithContext(s.ctx)
		ok, err := s.checkAcceptError(err)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if rw != nil {
			wg.Add(1)
			var cc *client.ClientConn
			monitor := s.createInactivityMonitor()
			opts := []coapNet.ConnOption{
				coapNet.WithHeartBeat(s.heartBeat),
				coapNet.WithOnReadTimeout(func() error {
					monitor.CheckInactivity(cc)
					return nil
				}),
			}
			cc = s.createClientConn(coapNet.NewConn(rw, opts...), monitor)
			if s.onNewClientConn != nil {
				dtlsConn := rw.(*dtls.Conn)
				s.onNewClientConn(cc, dtlsConn)
			}
			go func() {
				defer wg.Done()
				err := cc.Run()
				if err != nil {
					s.errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
				}
			}()
		}
	}
}

// Stop stops server without wait of ends Serve function.
func (s *Server) Stop() {
	s.cancel()
}

func (s *Server) createClientConn(connection *coapNet.Conn, monitor inactivity.Monitor) *client.ClientConn {
	var blockWise *blockwise.BlockWise
	if s.blockwiseEnable {
		blockWise = blockwise.NewBlockWise(
			bwAcquireMessage,
			bwReleaseMessage,
			s.blockwiseTransferTimeout,
			s.errors,
			false,
			func(token message.Token) (blockwise.Message, bool) {
				return nil, false
			},
		)
	}
	obsHandler := client.NewHandlerContainer()
	session := NewSession(
		s.ctx,
		connection,
		s.maxMessageSize,
		true,
	)
	cc := client.NewClientConn(
		session,
		obsHandler,
		kitSync.NewMap(),
		s.transmissionNStart,
		s.transmissionAcknowledgeTimeout,
		s.transmissionMaxRetransmit,
		client.NewObservationHandler(obsHandler, s.handler),
		s.blockwiseSZX,
		blockWise,
		s.goPool,
		s.errors,
		s.getMID,
		monitor,
	)

	return cc
}
