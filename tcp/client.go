package tcp

import (
	"context"
	"io"
	"net"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/mux"
	"github.com/go-ocf/go-coap/v2/tcp/message/pool"
)

type ClientTCP struct {
	cc *ClientConn
}

func NewClientTCP(cc *ClientConn) *ClientTCP {
	return &ClientTCP{
		cc: cc,
	}
}

func (cc *ClientTCP) Ping(ctx context.Context) error {
	return cc.cc.Ping(ctx)
}

func (cc *ClientTCP) Delete(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Delete(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *ClientTCP) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Put(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *ClientTCP) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Post(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *ClientTCP) Get(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Get(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return pool.ConvertTo(resp), err
}

func (cc *ClientTCP) Close() error {
	return cc.cc.Close()
}

func (cc *ClientTCP) RemoteAddr() net.Addr {
	return cc.cc.RemoteAddr()
}

func (cc *ClientTCP) Context() context.Context {
	return cc.cc.Context()
}

func (cc *ClientTCP) WriteRequest(req *message.Message) error {
	r, err := pool.ConvertFrom(req)
	if err != nil {
		return err
	}
	defer pool.ReleaseMessage(r)
	return cc.cc.WriteRequest(r)
}

func (cc *ClientTCP) Do(req *message.Message) (*message.Message, error) {
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

func (cc *ClientTCP) Observe(ctx context.Context, path string, observeFunc func(notification *message.Message), opts ...message.Option) (mux.Observation, error) {
	return cc.cc.Observe(ctx, path, func(n *pool.Message) {
		muxn := pool.ConvertTo(n)
		observeFunc(muxn)
	}, opts...)
}
