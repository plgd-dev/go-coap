package udp

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/keepalive"
	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapUDP "github.com/go-ocf/go-coap/v2/message/udp"
	coapNet "github.com/go-ocf/go-coap/v2/net"
)

var defaultDialOptions = dialOptions{
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
	dialer:       &net.Dialer{Timeout: time.Second * 3},
	keepalive:    keepalive.New(),
	net:          "udp",
	blockwiseSZX: blockwise.SZX16,
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

type dialOptions struct {
	ctx              context.Context
	maxMessageSize   int
	heartBeat        time.Duration
	handler          HandlerFunc
	errors           ErrorFunc
	goPool           GoPoolFunc
	dialer           *net.Dialer
	keepalive        *keepalive.KeepAlive
	net              string
	blockwiseSZX     blockwise.SZX
	blockWiseFactory BlockwiseFactoryFunc
}

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	applyDial(*dialOptions)
}

type ClientConn struct {
	noCopy
	session                 *Session
	observationTokenHandler *HandlerContainer
}

func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}

	c, err := cfg.dialer.DialContext(cfg.ctx, cfg.net, target)
	if err != nil {
		return nil, err
	}
	conn, ok := c.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("unsupported connection type: %T", c)
	}

	addr, ok := conn.RemoteAddr().(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("cannot get target upd address")
	}
	var blockWise *blockwise.BlockWise
	if cfg.blockWiseFactory != nil {
		blockWise = cfg.blockWiseFactory()
	}

	observationTokenHandler := NewHandlerContainer()

	l := coapNet.NewUDPConn(conn, coapNet.WithHeartBeat(cfg.heartBeat), coapNet.WithErrors(cfg.errors))
	cc := NewClientConn(NewSession(cfg.ctx,
		l,
		addr,
		NewObservatiomHandler(observationTokenHandler, cfg.handler),
		cfg.maxMessageSize,
		cfg.goPool,
		cfg.blockwiseSZX,
		blockWise,
	), observationTokenHandler)

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.errors(err)
		}
	}()
	if cfg.keepalive != nil {
		go func() {
			err := cfg.keepalive.Run(cc)
			if err != nil {
				cfg.errors(err)
			}
		}()
	}

	return cc, nil
}

func NewClientConn(session *Session, observationTokenHandler *HandlerContainer) *ClientConn {
	return &ClientConn{
		session:                 session,
		observationTokenHandler: observationTokenHandler,
	}
}

// Close closes client immediately.
func (cc *ClientConn) Close() error {
	return cc.session.Close()
}

func (cc *ClientConn) do(req *Message) (*Message, error) {
	token := req.Token()
	if token == nil {
		return nil, fmt.Errorf("invalid token")
	}
	respChan := make(chan *Message, 1)
	err := cc.session.TokenHandler().Insert(token, func(w *ResponseWriter, r *Message) {
		r.Hijack()
		respChan <- r
	})
	if err != nil {
		return nil, fmt.Errorf("cannot add token handler: %w", err)
	}
	defer cc.session.TokenHandler().Pop(token)
	err = cc.session.WriteRequest(req)
	if err != nil {
		return nil, fmt.Errorf("cannot write request: %w", err)
	}

	select {
	case <-req.ctx.Done():
		return nil, req.ctx.Err()
	case <-cc.session.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", req.ctx.Err())
	case resp := <-respChan:
		return resp, nil
	}
}

// Do sends an coap request and returns an coap response.
func (cc *ClientConn) Do(req *Message) (*Message, error) {
	if cc.session.blockWise == nil {
		return cc.do(req)
	}
	bwresp, err := cc.session.blockWise.Do(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, func(bwreq blockwise.Message) (blockwise.Message, error) {
		return cc.do(bwreq.(*Message))
	})
	return bwresp.(*Message), err
}

func (cc *ClientConn) writeRequest(req *Message) error {
	return cc.session.WriteRequest(req)
}

// WriteRequest sends an coap request.
func (cc *ClientConn) WriteRequest(req *Message) error {
	if cc.session.blockWise == nil {
		return cc.writeRequest(req)
	}
	return cc.session.blockWise.WriteRequest(req, cc.session.blockwiseSZX, cc.session.maxMessageSize, func(bwreq blockwise.Message) error {
		return cc.writeRequest(bwreq.(*Message))
	})
}

func (cc *ClientConn) doWithMID(req *Message) (*Message, error) {
	respChan := make(chan *Message, 1)
	err := cc.session.midHandlerContainer.Insert(req.MessageID(), func(w *ResponseWriter, r *Message) {
		r.Hijack()
		respChan <- r
	})
	if err != nil {
		return nil, fmt.Errorf("cannot insert mid handler: %w", err)
	}
	defer cc.session.midHandlerContainer.Pop(req.MessageID())
	err = cc.session.WriteRequest(req)
	if err != nil {
		return nil, fmt.Errorf("cannot write request: %w", err)
	}

	select {
	case <-req.ctx.Done():
		return nil, req.ctx.Err()
	case <-cc.session.Context().Done():
		return nil, fmt.Errorf("connection was closed: %w", req.ctx.Err())
	case resp := <-respChan:
		return resp, nil
	}
}

func newCommonRequest(ctx context.Context, code codes.Code, path string, queries ...string) (*Message, error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireRequest(ctx)
	req.SetCode(code)
	req.SetToken(token)
	req.SetPath(path)
	for _, q := range queries {
		req.AddQuery(q)
	}
	return req, nil
}

// NewGetRequest creates get request.
func NewGetRequest(ctx context.Context, path string, queries ...string) (*Message, error) {
	return newCommonRequest(ctx, codes.GET, path, queries...)
}

// Get issues a GET to the specified path.
func (cc *ClientConn) Get(ctx context.Context, path string, queries ...string) (*Message, error) {
	req, err := NewGetRequest(ctx, path, queries...)
	if err != nil {
		return nil, fmt.Errorf("cannot create get request: %w", err)
	}
	defer ReleaseRequest(req)
	return cc.Do(req)
}

// NewPostRequest creates post request.
func NewPostRequest(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, queries ...string) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.POST, path, queries...)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.SetContentFormat(contentFormat)
		req.SetPayload(payload)
	}
	return req, nil
}

// Post issues a POST to the specified path.
func (cc *ClientConn) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, queries ...string) (*Message, error) {
	req, err := NewPostRequest(ctx, path, contentFormat, payload, queries...)
	if err != nil {
		return nil, fmt.Errorf("cannot create post request: %w", err)
	}
	defer ReleaseRequest(req)
	return cc.Do(req)
}

// NewPutRequest creates put request.
func NewPutRequest(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, queries ...string) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.PUT, path, queries...)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.SetContentFormat(contentFormat)
		req.SetPayload(payload)
	}
	return req, nil
}

// Put issues a POST to the specified path.
func (cc *ClientConn) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, queries ...string) (*Message, error) {
	req, err := NewPutRequest(ctx, path, contentFormat, payload, queries...)
	if err != nil {
		return nil, fmt.Errorf("cannot create put request: %w", err)
	}
	defer ReleaseRequest(req)
	return cc.Do(req)
}

// Delete deletes the resource identified by the request path.
func (cc *ClientConn) Delete(ctx context.Context, path string) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.DELETE, path)
	if err != nil {
		return nil, fmt.Errorf("cannot create delete request: %w", err)
	}
	defer ReleaseRequest(req)
	return cc.Do(req)
}

// Context returns the client's context.
func (cc *ClientConn) Context() context.Context {
	return cc.session.Context()
}

// Ping issues a PING to the client.
func (cc *ClientConn) Ping(ctx context.Context) error {
	req := AcquireRequest(ctx)
	defer ReleaseRequest(req)
	req.SetType(coapUDP.Confirmable)
	req.SetCode(codes.Empty)
	req.SetMessageID(cc.session.getMID())
	resp, err := cc.doWithMID(req)
	if err != nil {
		return err
	}
	defer ReleaseRequest(resp)
	if resp.Type() == coapUDP.Reset || resp.Type() == coapUDP.Acknowledgement {
		return nil
	}
	return fmt.Errorf("unexpected response(%v)", resp)
}

func (cc *ClientConn) Run() error {
	m := make([]byte, cc.session.maxMessageSize)
	for {
		buf := m
		n, _, err := cc.session.connection.ReadWithContext(cc.session.ctx, buf)
		if err != nil {
			cc.session.Close()
			return err
		}
		buf = buf[:n]
		err = cc.session.processBuffer(buf, cc)
		if err != nil {
			cc.session.Close()
			return err
		}
	}
}

// AddOnClose calls function on close connection event.
func (cc *ClientConn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

func (cc *ClientConn) processBuffer(buffer []byte) error {
	return cc.session.processBuffer(buffer, cc)
}
