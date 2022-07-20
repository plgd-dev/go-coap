package tcp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/observation"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	coapErrors "github.com/plgd-dev/go-coap/v2/pkg/errors"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
)

var defaultDialOptions = func() dialOptions {
	opts := dialOptions{
		ctx:            context.Background(),
		maxMessageSize: 64 * 1024,
		errors: func(err error) {
			fmt.Println(err)
		},
		goPool: func(f func()) error {
			go func() {
				f()
			}()
			return nil
		},
		dialer:                   &net.Dialer{Timeout: time.Second * 3},
		net:                      "tcp",
		blockwiseSZX:             blockwise.SZX1024,
		blockwiseEnable:          true,
		blockwiseTransferTimeout: time.Second * 3,
		createInactivityMonitor: func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		},
		periodicRunner: func(f func(now time.Time) bool) {
			go func() {
				for f(time.Now()) {
					time.Sleep(4 * time.Second)
				}
			}()
		},
		connectionCacheSize: 2048,
		messagePool:         pool.New(1024, 2048),
		getToken:            message.GetToken,
	}
	opts.handler = func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
				opts.errors(fmt.Errorf("client handler: cannot set response: %w", err))
			}
		}
	}
	return opts
}()

type dialOptions struct {
	ctx                             context.Context
	net                             string
	blockwiseTransferTimeout        time.Duration
	messagePool                     *pool.Pool
	goPool                          GoPoolFunc
	dialer                          *net.Dialer
	tlsCfg                          *tls.Config
	periodicRunner                  periodic.Func
	createInactivityMonitor         func() inactivity.Monitor
	handler                         HandlerFunc
	errors                          ErrorFunc
	getToken                        client.GetTokenFunc
	maxMessageSize                  uint32
	connectionCacheSize             uint16
	disablePeerTCPSignalMessageCSMs bool
	closeSocket                     bool
	blockwiseEnable                 bool
	blockwiseSZX                    blockwise.SZX
	disableTCPSignalMessageCSM      bool
}

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	applyDial(*dialOptions)
}

type Notifier interface {
	Notify()
}

// ClientConn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type ClientConn struct {
	noCopy
	*client.Client[*ClientConn]
	session *Session
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}

	var conn net.Conn
	var err error
	if cfg.tlsCfg != nil {
		conn, err = tls.DialWithDialer(cfg.dialer, cfg.net, target, cfg.tlsCfg)
	} else {
		conn, err = cfg.dialer.DialContext(cfg.ctx, cfg.net, target)
	}
	if err != nil {
		return nil, err
	}
	opts = append(opts, WithCloseSocket())
	return Client(conn, opts...), nil
}

// Client creates client over tcp/tcp-tls connection.
func Client(conn net.Conn, opts ...DialOption) *ClientConn {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}
	if cfg.errors == nil {
		cfg.errors = func(error) {
			// default no-op
		}
	}
	if cfg.createInactivityMonitor == nil {
		cfg.createInactivityMonitor = func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		}
	}
	if cfg.messagePool == nil {
		cfg.messagePool = pool.New(0, 0)
	}
	errorsFunc := cfg.errors
	cfg.errors = func(err error) {
		if coapNet.IsCancelOrCloseError(err) {
			// this error was produced by cancellation context or closing connection.
			return
		}
		errorsFunc(fmt.Errorf("tcp: %w", err))
	}

	createBlockWise := func(cc *ClientConn) *blockwise.BlockWise[*ClientConn] {
		return nil
	}
	if cfg.blockwiseEnable {
		createBlockWise = func(cc *ClientConn) *blockwise.BlockWise[*ClientConn] {
			return blockwise.New(
				cc,
				cfg.blockwiseTransferTimeout,
				cfg.errors,
				false,
				cc.GetObservationRequest,
			)
		}
	}

	l := coapNet.NewConn(conn)
	monitor := cfg.createInactivityMonitor()
	cc := NewClientConn(cfg.ctx,
		l,
		cfg.handler,
		cfg.maxMessageSize,
		cfg.goPool,
		cfg.errors,
		cfg.blockwiseSZX,
		createBlockWise,
		cfg.disablePeerTCPSignalMessageCSMs,
		cfg.disableTCPSignalMessageCSM,
		cfg.closeSocket,
		monitor,
		cfg.connectionCacheSize,
		cfg.messagePool,
		cfg.getToken,
	)

	cfg.periodicRunner(func(now time.Time) bool {
		cc.CheckExpirations(now)
		return cc.Context().Err() == nil
	})

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.errors(fmt.Errorf("%v: %w", cc.RemoteAddr(), err))
		}
	}()

	return cc
}

// NewClientConn creates connection over session and observation.
func NewClientConn(ctx context.Context,
	connection *coapNet.Conn,
	handler func(*responsewriter.ResponseWriter[*ClientConn], *pool.Message),
	maxMessageSize uint32,
	goPool GoPoolFunc,
	errors ErrorFunc,
	blockwiseSZX blockwise.SZX,
	createBlockWise func(cc *ClientConn) *blockwise.BlockWise[*ClientConn],
	disablePeerTCPSignalMessageCSMs bool,
	disableTCPSignalMessageCSM bool,
	closeSocket bool,
	inactivityMonitor inactivity.Monitor,
	connectionCacheSize uint16,
	messagePool *pool.Pool,
	getToken client.GetTokenFunc,
) *ClientConn {
	if getToken == nil {
		getToken = message.GetToken
	}
	cc := ClientConn{}
	observationHandler := observation.NewHandler(&cc, handler)
	cc.Client = client.New(&cc, observationHandler, getToken)
	blockWise := createBlockWise(&cc)
	session := NewSession(ctx,
		connection,
		observationHandler.Handle,
		maxMessageSize,
		goPool,
		errors,
		blockwiseSZX,
		blockWise,
		disablePeerTCPSignalMessageCSMs,
		disableTCPSignalMessageCSM,
		closeSocket,
		inactivityMonitor,
		connectionCacheSize,
		messagePool,
	)
	cc.session = session
	return &cc
}

func (cc *ClientConn) Session() *Session {
	return cc.session
}

// Close closes connection without wait of ends Run function.
func (cc *ClientConn) Close() error {
	err := cc.session.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (cc *ClientConn) do(req *pool.Message) (*pool.Message, error) {
	token := req.Token()
	if token == nil {
		return nil, fmt.Errorf("invalid token")
	}
	respChan := make(chan *pool.Message, 1)
	if _, loaded := cc.session.TokenHandler().LoadOrStore(token.Hash(), func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		r.Hijack()
		select {
		case respChan <- r:
		default:
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	defer func() {
		_, _ = cc.session.TokenHandler().PullOut(token.Hash())
	}()
	if err := cc.session.WriteMessage(req); err != nil {
		return nil, fmt.Errorf("cannot write request: %w", err)
	}

	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-cc.session.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", cc.Context().Err())
	case resp := <-respChan:
		return resp, nil
	}
}

// Do sends an coap message and returns an coap response.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// Caller is responsible to release request and response.
func (cc *ClientConn) Do(req *pool.Message) (*pool.Message, error) {
	if !cc.session.PeerBlockWiseTransferEnabled() || cc.session.blockWise == nil {
		return cc.do(req)
	}
	resp, err := cc.session.blockWise.Do(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, cc.do)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (cc *ClientConn) writeMessage(req *pool.Message) error {
	return cc.session.WriteMessage(req)
}

// WriteMessage sends an coap message.
func (cc *ClientConn) WriteMessage(req *pool.Message) error {
	if !cc.session.PeerBlockWiseTransferEnabled() || cc.session.blockWise == nil {
		return cc.writeMessage(req)
	}
	return cc.session.blockWise.WriteMessage(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, cc.writeMessage)
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *ClientConn) Context() context.Context {
	return cc.session.Context()
}

// AsyncPing sends ping and receivedPong will be called when pong arrives. It returns cancellation of ping operation.
func (cc *ClientConn) AsyncPing(receivedPong func()) (func(), error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := cc.session.messagePool.AcquireMessage(cc.Context())
	req.SetToken(token)
	req.SetCode(codes.Ping)
	defer cc.ReleaseMessage(req)

	if _, loaded := cc.session.TokenHandler().LoadOrStore(token.Hash(), func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		if r.Code() == codes.Pong {
			receivedPong()
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	removeTokenHandler := func() {
		_, _ = cc.session.TokenHandler().PullOut(token.Hash())
	}
	err = cc.session.WriteMessage(req)
	if err != nil {
		removeTokenHandler()
		return nil, fmt.Errorf("cannot write request: %w", err)
	}
	return removeTokenHandler, nil
}

// Run reads and process requests from a connection, until the connection is not closed.
func (cc *ClientConn) Run() (err error) {
	return cc.session.Run(cc)
}

// AddOnClose calls function on close connection event.
func (cc *ClientConn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

// RemoteAddr gets remote address.
func (cc *ClientConn) RemoteAddr() net.Addr {
	return cc.session.RemoteAddr()
}

func (cc *ClientConn) LocalAddr() net.Addr {
	return cc.session.LocalAddr()
}

// Sequence acquires sequence number.
func (cc *ClientConn) Sequence() uint64 {
	return cc.session.Sequence()
}

// SetContextValue stores the value associated with key to context of connection.
func (cc *ClientConn) SetContextValue(key interface{}, val interface{}) {
	cc.session.SetContextValue(key, val)
}

// Done signalizes that connection is not more processed.
func (cc *ClientConn) Done() <-chan struct{} {
	return cc.session.Done()
}

// CheckExpirations checks and remove expired items from caches.
func (cc *ClientConn) CheckExpirations(now time.Time) {
	cc.session.CheckExpirations(now, cc)
}

func (cc *ClientConn) AcquireMessage(ctx context.Context) *pool.Message {
	return cc.session.AcquireMessage(ctx)
}

func (cc *ClientConn) ReleaseMessage(m *pool.Message) {
	cc.session.ReleaseMessage(m)
}
