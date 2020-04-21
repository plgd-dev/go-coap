package tcp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

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
}

type serverOptions struct {
	ctx                             context.Context
	maxMessageSize                  int
	heartBeat                       time.Duration
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool
}

type Server struct {
	maxMessageSize                  int
	heartBeat                       time.Duration
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool

	serveWG sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
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

	return &Server{
		ctx:     ctx,
		cancel:  cancel,
		handler: handler,
	}
}

func (s *Server) Serve(l Listener) error {
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
			go func() {
				defer wg.Done()
				session := NewSession(s.ctx,
					coapNet.NewConn(rw, s.heartBeat),
					s.handler,
					s.maxMessageSize,
					s.disablePeerTCPSignalMessageCSMs,
					s.disableTCPSignalMessageCSM,
					s.goPool)
				err := session.Run()
				if err != nil {
					s.errors(err)
				}
			}()
		}
	}
}
func (s *Server) Stop() {
	s.cancel()
	defer s.serveWG.Wait()
}
