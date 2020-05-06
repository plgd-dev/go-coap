package tcp

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/mux"
	"github.com/go-ocf/go-coap/v2/tcp/message/pool"
)

// WithMux set's multiplexer for handle requests.
func WithMux(m mux.Handler) HandlerFuncOpt {
	h := func(w *ResponseWriter, r *pool.Message) {
		muxw := &muxResponseWriter{
			w: w,
		}
		muxr := msg2muxmsg(r)
		m.ServeCOAP(muxw, muxr)
	}
	return WithHandlerFunc(h)
}

type muxResponseWriter struct {
	w *ResponseWriter
}

func (w *muxResponseWriter) SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error {
	return w.w.SetResponse(code, contentFormat, d, opts...)
}

func (w *muxResponseWriter) ClientConn() mux.ClientConn {
	return NewMuxClientConn(w.w.ClientConn())
}

type MuxClientConn struct {
	cc *ClientConn
}

func NewMuxClientConn(cc *ClientConn) *MuxClientConn {
	return &MuxClientConn{
		cc: cc,
	}
}

func (cc *MuxClientConn) Ping(ctx context.Context) error {
	return cc.cc.Ping(ctx)
}

func msg2muxmsg(m *pool.Message) *message.Message {
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

func (cc *MuxClientConn) Delete(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Delete(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return msg2muxmsg(resp), err
}

func (cc *MuxClientConn) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Put(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return msg2muxmsg(resp), err
}

func (cc *MuxClientConn) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Post(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return msg2muxmsg(resp), err
}

func (cc *MuxClientConn) Get(ctx context.Context, path string, opts ...message.Option) (*message.Message, error) {
	resp, err := cc.cc.Get(ctx, path, opts...)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return msg2muxmsg(resp), err
}

func (cc *MuxClientConn) Close() error {
	return cc.cc.Close()
}

func muxmsg2msg(m *message.Message) (*pool.Message, error) {
	if m.Context == nil {
		return nil, fmt.Errorf("invalid context")
	}
	r := pool.AcquireMessage(m.Context)
	r.SetCode(m.Code)
	r.ResetOptionsTo(m.Options)
	r.SetPayload(m.Body)
	r.SetToken(m.Token)
	return r, nil
}

func (cc *MuxClientConn) WriteRequest(req *message.Message) error {
	r, err := muxmsg2msg(req)
	if err != nil {
		return err
	}
	defer pool.ReleaseMessage(r)
	return cc.cc.WriteRequest(r)
}

func (cc *MuxClientConn) Do(req *message.Message) (*message.Message, error) {
	r, err := muxmsg2msg(req)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(r)
	resp, err := cc.cc.Do(r)
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseMessage(resp)
	return msg2muxmsg(resp), err
}

func (cc *MuxClientConn) Observe(ctx context.Context, path string, observeFunc func(notification *message.Message), opts ...message.Option) (mux.Observation, error) {
	return cc.cc.Observe(ctx, path, func(n *pool.Message) {
		muxn := msg2muxmsg(n)
		observeFunc(muxn)
	}, opts...)
}

func (cc *MuxClientConn) RemoteAddr() net.Addr {
	return cc.cc.RemoteAddr()
}

func (cc *MuxClientConn) Context() context.Context {
	return cc.cc.Context()
}
