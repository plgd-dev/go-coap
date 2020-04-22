package tcp

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapNet "github.com/go-ocf/go-coap/v2/net"
)

var defaultDialOptions = dialOptions{
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
	dialer: &net.Dialer{Timeout: time.Second * 3},
}

type dialOptions struct {
	ctx                             context.Context
	maxMessageSize                  int
	heartBeat                       time.Duration
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool
	dialer                          *net.Dialer
}

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	applyDial(*dialOptions)
}

type ClientConn struct {
	session *Session
}

func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}
	conn, err := cfg.dialer.DialContext(cfg.ctx, "tcp", target)
	if err != nil {
		return nil, err
	}
	session := NewSession(cfg.ctx,
		coapNet.NewConn(conn, cfg.heartBeat),
		cfg.handler,
		cfg.maxMessageSize,
		cfg.disablePeerTCPSignalMessageCSMs,
		cfg.disableTCPSignalMessageCSM,
		cfg.goPool,
	)
	go func() {
		err = session.Run()
		if err != nil {
			cfg.errors(err)
		}
	}()

	return NewClientConn(session), nil
}

func NewClientConn(session *Session) *ClientConn {
	return &ClientConn{
		session: session,
	}
}

func (cc *ClientConn) Close() error {
	return cc.session.Close()
}

func (cc *ClientConn) Do(req *Request) (*Request, error) {
	token := req.Token()
	if token == nil {
		return nil, fmt.Errorf("invalid token")
	}
	respChan := make(chan *Request, 1)
	err := cc.session.TokenHandler().Add(token, func(w *ResponseWriter, r *Request) {
		r.Hijack()
		respChan <- r
	})
	if err != nil {
		return nil, fmt.Errorf("cannot add token handler: %w", err)
	}
	defer cc.session.TokenHandler().Remove(token)
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

func NewGetRequest(ctx context.Context, path string, queries ...string) (*Request, error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireRequest(ctx)
	req.SetCode(codes.GET)
	req.SetToken(token)
	req.SetPath(path)
	for _, q := range queries {
		req.AddQuery(q)
	}
	return req, nil
}

func (cc *ClientConn) Get(ctx context.Context, path string, queries ...string) (*Request, error) {
	req, err := NewGetRequest(ctx, path, queries...)
	if err != nil {
		return nil, fmt.Errorf("cannot create get request: %w", err)
	}
	defer ReleaseRequest(req)
	return cc.Do(req)
}

func (cc *ClientConn) Ping(ctx context.Context) error {
	token, err := message.GetToken()
	if err != nil {
		return fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireRequest(ctx)
	req.SetToken(token)
	req.SetCode(codes.Ping)
	defer ReleaseRequest(req)
	resp, err := cc.Do(req)
	if err != nil {
		return err
	}
	if resp.Code() == codes.Pong {
		return nil
	}
	return fmt.Errorf("unexpected code(%v)", resp.Code())
}
