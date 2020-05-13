package client

import (
	"context"
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

func (cc *Client) Delete(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Delete(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *Client) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Put(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *Client) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Post(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *Client) Get(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Get(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
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

func (cc *Client) WriteMessage(req *message.Message) error {
	r, err := pool.ConvertFrom(req)
	if err != nil {
		return err
	}
	r.SetMessageID(cc.cc.GetMID())
	defer pool.ReleaseMessage(r)
	return cc.cc.WriteMessage(r)
}

func (cc *Client) Do(req *message.Message) (*message.Message, error) {
	r, err := pool.ConvertFrom(req)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(r)
	resp, err := cc.cc.Do(r)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *Client) Observe(ctx context.Context, path string, observeFunc func(notification *message.Message), opts ...message.Option) (mux.Observation, error) {
	return cc.cc.Observe(ctx, path, func(n *pool.Message) {
		muxn := pool.ConvertTo(n)
		observeFunc(muxn)
	}, opts...)
}
