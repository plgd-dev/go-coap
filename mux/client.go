package mux

import (
	"context"
	"io"
	"net"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
)

type Observation = interface {
	Cancel(ctx context.Context) error
	Canceled() bool
}

type Client interface {
	// create message from pool
	AcquireMessage(ctx context.Context) *pool.Message
	// return back the message to the pool for next use
	ReleaseMessage(m *pool.Message)

	Ping(ctx context.Context) error
	Get(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error)
	Delete(ctx context.Context, path string, opts ...message.Option) (*pool.Message, error)
	Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error)
	Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*pool.Message, error)
	Observe(ctx context.Context, path string, observeFunc func(notification *pool.Message), opts ...message.Option) (Observation, error)

	RemoteAddr() net.Addr
	Context() context.Context
	SetContextValue(key interface{}, val interface{})
	WriteMessage(req *pool.Message) error
	Do(req *pool.Message) (*pool.Message, error)
	Close() error
	Sequence() uint64
	// Done signalizes that connection is not more processed.
	Done() <-chan struct{}
}
