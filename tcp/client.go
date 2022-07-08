package tcp

import (
	"context"
	"io"
	"net"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
)

type ClientTCP struct {
	cc *ClientConn
}

func NewClientTCP(cc *ClientConn) *ClientTCP {
	return &ClientTCP{
		cc: cc,
	}
}

func (c *ClientTCP) Ping(ctx context.Context) error {
	return c.cc.Ping(ctx)
}

func (c *ClientTCP) Delete(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Delete(ctx, path, opts...)
}

func (c *ClientTCP) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Put(ctx, path, contentFormat, payload, opts...)
}

func (c *ClientTCP) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Post(ctx, path, contentFormat, payload, opts...)
}

func (c *ClientTCP) Get(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error) {
	return c.cc.Get(ctx, path, opts...)
}

func (c *ClientTCP) Close() error {
	return c.cc.Close()
}

func (c *ClientTCP) RemoteAddr() net.Addr {
	return c.cc.RemoteAddr()
}

func (c *ClientTCP) Context() context.Context {
	return c.cc.Context()
}

func (c *ClientTCP) SetContextValue(key interface{}, val interface{}) {
	c.cc.Session().SetContextValue(key, val)
}

func (c *ClientTCP) WriteMessage(req *pool.Message) error {
	return c.cc.WriteMessage(req)
}

func (c *ClientTCP) Do(req *pool.Message) (*pool.Message, error) {
	return c.cc.Do(req)
}

func (c *ClientTCP) Observe(ctx context.Context, path string, observeFunc func(notification *pool.Message), opts ...message.Option) (mux.Observation, error) {
	return c.cc.Observe(ctx, path, observeFunc, opts...)
}

// Sequence acquires sequence number.
func (c *ClientTCP) Sequence() uint64 {
	return c.cc.Sequence()
}

// ClientConn get's underlaying client connection.
func (c *ClientTCP) ClientConn() interface{} {
	return c.cc
}

// Done signalizes that connection is not more processed.
func (c *ClientTCP) Done() <-chan struct{} {
	return c.cc.Done()
}

func (c *ClientTCP) AcquireMessage(ctx context.Context) *pool.Message {
	return c.cc.AcquireMessage(ctx)
}

func (c *ClientTCP) ReleaseMessage(m *pool.Message) {
	c.cc.ReleaseMessage(m)
}
