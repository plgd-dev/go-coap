package client

import (
	"context"
	"io"
	"net"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
)

type Client struct {
	cc *ClientConn
}

func NewClient(cc *ClientConn) *Client {
	return &Client{
		cc: cc,
	}
}

func (c *Client) Ping(ctx context.Context) error {
	return c.cc.Ping(ctx)
}

func (c *Client) Delete(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Delete(ctx, path, opts...)
}

func (c *Client) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Put(ctx, path, contentFormat, payload, opts...)
}

func (c *Client) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Post(ctx, path, contentFormat, payload, opts...)
}

func (c *Client) Get(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Get(ctx, path, opts...)
}

func (c *Client) Close() error {
	return c.cc.Close()
}

func (c *Client) RemoteAddr() net.Addr {
	return c.cc.RemoteAddr()
}

func (c *Client) Context() context.Context {
	return c.cc.Context()
}

func (c *Client) SetContextValue(key interface{}, val interface{}) {
	c.cc.Session().SetContextValue(key, val)
}

func (c *Client) WriteMessage(req *pool.Message) error {
	return c.cc.WriteMessage(req)
}

func (c *Client) Do(req *pool.Message) (*pool.Message, error) {
	return c.cc.Do(req)
}

func (c *Client) Observe(ctx context.Context, path string, observeFunc func(notification *pool.Message), opts ...message.Option) (mux.Observation, error) {
	return c.cc.Observe(ctx, path, observeFunc, opts...)
}

// Sequence acquires sequence number.
func (c *Client) Sequence() uint64 {
	return c.cc.Sequence()
}

// ClientConn get's underlaying client connection.
func (c *Client) ClientConn() interface{} {
	return c.cc
}

// Done signalizes that connection is not more processed.
func (c *Client) Done() <-chan struct{} {
	return c.cc.Done()
}

func (c *Client) AcquireMessage(ctx context.Context) *pool.Message {
	return c.cc.AcquireMessage(ctx)
}

func (c *Client) ReleaseMessage(m *pool.Message) {
	c.cc.ReleaseMessage(m)
}
