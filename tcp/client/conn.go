package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/client"
	limitparallelrequests "github.com/plgd-dev/go-coap/v3/net/client/limitParallelRequests"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
)

type InactivityMonitor interface {
	Notify()
	CheckInactivity(now time.Time, cc *Conn)
}

type (
	HandlerFunc                 = func(*responsewriter.ResponseWriter[*Conn], *pool.Message)
	ErrorFunc                   = func(error)
	GoPoolFunc                  = func(func()) error
	EventFunc                   = func()
	GetMIDFunc                  = func() int32
	CreateInactivityMonitorFunc = func() InactivityMonitor
)

type Notifier interface {
	Notify()
}

// Conn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type Conn struct {
	*client.Client[*Conn]
	session            *Session
	observationHandler *observation.Handler[*Conn]
}

// NewConn creates connection over session and observation.
func NewConn(
	connection *coapNet.Conn,
	createBlockWise func(cc *Conn) *blockwise.BlockWise[*Conn],
	inactivityMonitor InactivityMonitor,
	cfg *Config,
) *Conn {
	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}
	cc := Conn{}
	limitParallelRequests := limitparallelrequests.New(cfg.LimitClientParallelRequests, cfg.LimitClientEndpointParallelRequests, cc.do, cc.doObserve)
	cc.observationHandler = observation.NewHandler(&cc, cfg.Handler, limitParallelRequests.Do)
	cc.Client = client.New(&cc, cc.observationHandler, cfg.GetToken, limitParallelRequests)
	blockWise := createBlockWise(&cc)
	session := NewSession(cfg.Ctx,
		connection,
		cc.observationHandler.Handle,
		cfg.MaxMessageSize,
		cfg.GoPool,
		cfg.Errors,
		cfg.BlockwiseSZX,
		blockWise,
		cfg.DisablePeerTCPSignalMessageCSMs,
		cfg.DisableTCPSignalMessageCSM,
		cfg.CloseSocket,
		inactivityMonitor,
		cfg.ConnectionCacheSize,
		cfg.MessagePool,
	)
	cc.session = session
	return &cc
}

func (cc *Conn) Session() *Session {
	return cc.session
}

// Close closes connection without wait of ends Run function.
func (cc *Conn) Close() error {
	err := cc.session.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (cc *Conn) doInternal(req *pool.Message) (*pool.Message, error) {
	token := req.Token()
	if token == nil {
		return nil, fmt.Errorf("invalid token")
	}
	respChan := make(chan *pool.Message, 1)
	if _, loaded := cc.session.TokenHandler().LoadOrStore(token.Hash(), func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		r.Hijack()
		select {
		case respChan <- r:
		default:
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	defer func() {
		_, _ = cc.session.TokenHandler().LoadAndDelete(token.Hash())
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
func (cc *Conn) do(req *pool.Message) (*pool.Message, error) {
	if !cc.session.PeerBlockWiseTransferEnabled() || cc.session.blockWise == nil {
		return cc.doInternal(req)
	}
	resp, err := cc.session.blockWise.Do(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, cc.doInternal)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (cc *Conn) writeMessage(req *pool.Message) error {
	return cc.session.WriteMessage(req)
}

// WriteMessage sends an coap message.
func (cc *Conn) WriteMessage(req *pool.Message) error {
	if !cc.session.PeerBlockWiseTransferEnabled() || cc.session.blockWise == nil {
		return cc.writeMessage(req)
	}
	return cc.session.blockWise.WriteMessage(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, cc.writeMessage)
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *Conn) Context() context.Context {
	return cc.session.Context()
}

// AsyncPing sends ping and receivedPong will be called when pong arrives. It returns cancellation of ping operation.
func (cc *Conn) AsyncPing(receivedPong func()) (func(), error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := cc.session.messagePool.AcquireMessage(cc.Context())
	req.SetToken(token)
	req.SetCode(codes.Ping)
	defer cc.ReleaseMessage(req)

	if _, loaded := cc.session.TokenHandler().LoadOrStore(token.Hash(), func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		if r.Code() == codes.Pong {
			receivedPong()
		}
	}); loaded {
		return nil, fmt.Errorf("cannot add token handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	removeTokenHandler := func() {
		_, _ = cc.session.TokenHandler().LoadAndDelete(token.Hash())
	}
	err = cc.session.WriteMessage(req)
	if err != nil {
		removeTokenHandler()
		return nil, fmt.Errorf("cannot write request: %w", err)
	}
	return removeTokenHandler, nil
}

// Run reads and process requests from a connection, until the connection is not closed.
func (cc *Conn) Run() (err error) {
	return cc.session.Run(cc)
}

// AddOnClose calls function on close connection event.
func (cc *Conn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

// RemoteAddr gets remote address.
func (cc *Conn) RemoteAddr() net.Addr {
	return cc.session.RemoteAddr()
}

func (cc *Conn) LocalAddr() net.Addr {
	return cc.session.LocalAddr()
}

// Sequence acquires sequence number.
func (cc *Conn) Sequence() uint64 {
	return cc.session.Sequence()
}

// SetContextValue stores the value associated with key to context of connection.
func (cc *Conn) SetContextValue(key interface{}, val interface{}) {
	cc.session.SetContextValue(key, val)
}

// Done signalizes that connection is not more processed.
func (cc *Conn) Done() <-chan struct{} {
	return cc.session.Done()
}

// CheckExpirations checks and remove expired items from caches.
func (cc *Conn) CheckExpirations(now time.Time) {
	cc.session.CheckExpirations(now, cc)
}

func (cc *Conn) AcquireMessage(ctx context.Context) *pool.Message {
	return cc.session.AcquireMessage(ctx)
}

func (cc *Conn) ReleaseMessage(m *pool.Message) {
	cc.session.ReleaseMessage(m)
}

// NetConn returns the underlying connection that is wrapped by cc. The Conn returned is shared by all invocations of NetConn, so do not modify it.
func (cc *Conn) NetConn() net.Conn {
	return cc.session.NetConn()
}

// DoObserve subscribes for every change with request.
func (cc *Conn) doObserve(req *pool.Message, observeFunc func(req *pool.Message), opts ...message.Option) (client.Observation, error) {
	return cc.observationHandler.NewObservation(req, observeFunc)
}
