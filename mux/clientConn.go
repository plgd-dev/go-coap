package mux

import (
	"context"
	"io"

	"github.com/go-ocf/go-coap/v2/message"
)

type Observation = interface {
	Cancel(ctx context.Context) error
}

type ClientConn interface {
	Ping(ctx context.Context) error
	Get(ctx context.Context, path string, opts ...message.Option) (*Message, error)
	Delete(ctx context.Context, path string, opts ...message.Option) (*Message, error)
	Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error)
	Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error)
	Observe(ctx context.Context, path string, observeFunc func(notification *Message), opts ...message.Option) (Observation, error)

	WriteRequest(req *Message) error
	Do(req *Message) (*Message, error)
	Close() error
}
