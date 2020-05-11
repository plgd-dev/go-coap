package client

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/mux"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

type Client struct {
	cc *ClientConn
}

func NewClient(cc *ClientConn) *Client {
	return &Client{
		cc: cc,
	}
}

func (cc *Client) Ping(ctx context.Context) error {
	return cc.cc.Ping(ctx)
}

func poolmsg2msg(m *pool.Message) *message.Message {
	opts := make(message.Options, 0, len(m.Options()))
	buf := make([]byte, 64)
	opts, used, err := opts.ResetOptionsTo(buf, m.Options())
	if err == message.ErrTooSmall {
		buf = append(buf, make([]byte, used-len(buf))...)
		opts, used, err = opts.ResetOptionsTo(buf, m.Options())
	}
	return &message.Message{
		Context: m.Context(),
		Code:    m.Code(),
		Token:   m.Token(),
		Body:    m.Body(),
		Options: opts,
	}
}

func (cc *Client) Delete(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Delete(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return poolmsg2msg(resp), err
}

func (cc *Client) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Put(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return poolmsg2msg(resp), err
}

func (cc *Client) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Post(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return poolmsg2msg(resp), err
}

func (cc *Client) Get(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Get(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return poolmsg2msg(resp), err
}

func (cc *Client) Close() error {
	return cc.cc.Close()
}

func (cc *Client) RemoteAddr() net.Addr {
	return cc.cc.RemoteAddr()
}

func (cc *Client) Context() context.Context {
	return cc.cc.Context()
}

func msg2poolmsg(m *message.Message) (*pool.Message, error) {
	if m.Context == nil {
		return nil, fmt.Errorf("invalid context")
	}
	r := pool.AcquireMessage(m.Context)
	r.SetCode(m.Code)
	r.ResetOptionsTo(m.Options)
	r.SetBody(m.Body)
	r.SetToken(m.Token)
	return r, nil
}

func (cc *Client) WriteRequest(req *message.Message) error {
	r, err := msg2poolmsg(req)
	if err != nil {
		return err
	}
	defer pool.ReleaseMessage(r)
	return cc.cc.WriteRequest(r)
}

func (cc *Client) Do(req *message.Message) (*message.Message, error) {
	r, err := msg2poolmsg(req)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(r)
	resp, err := cc.cc.Do(r)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return poolmsg2msg(resp), err
}

func (cc *Client) Observe(ctx context.Context, path string, observeFunc func(notification *message.Message), opts ...message.Option) (mux.Observation, error) {
	return cc.cc.Observe(ctx, path, func(n *pool.Message) {
		muxn := poolmsg2msg(n)
		observeFunc(muxn)
	}, opts...)
}
