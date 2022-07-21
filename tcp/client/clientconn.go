package client

import (
	"context"
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
)

type (
	HandlerFunc = func(*responsewriter.ResponseWriter[*ClientConn], *pool.Message)
	ErrorFunc   = func(error)
	GoPoolFunc  = func(func()) error
	EventFunc   = func()
	GetMIDFunc  = func() int32
)

type Notifier interface {
	Notify()
}

// ClientConn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type ClientConn struct {
	*client.Client[*ClientConn]
	session *Session
}

// NewClientConn creates connection over session and observation.
func NewClientConn(
	connection *coapNet.Conn,
	createBlockWise func(cc *ClientConn) *blockwise.BlockWise[*ClientConn],
	inactivityMonitor inactivity.Monitor,
	cfg *Config,
	/*ctx context.Context,

	handler func(*responsewriter.ResponseWriter[*ClientConn], *pool.Message),
	maxMessageSize uint32,
	goPool GoPoolFunc,
	errors ErrorFunc,
	blockwiseSZX blockwise.SZX,
	disablePeerTCPSignalMessageCSMs bool,
	disableTCPSignalMessageCSM bool,
	closeSocket bool,
	inactivityMonitor inactivity.Monitor,
	connectionCacheSize uint16,
	messagePool *pool.Pool,
	getToken client.GetTokenFunc,
	*/
) *ClientConn {
	if cfg.GetToken == nil {
		cfg.GetToken = message.GetToken
	}
	cc := ClientConn{}
	observationHandler := observation.NewHandler(&cc, cfg.Handler)
	cc.Client = client.New(&cc, observationHandler, cfg.GetToken)
	blockWise := createBlockWise(&cc)
	session := NewSession(cfg.Ctx,
		connection,
		observationHandler.Handle,
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
