package tcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/keepalive"
	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapNet "github.com/go-ocf/go-coap/v2/net"
)

var defaultDialOptions = dialOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	heartBeat:      time.Millisecond * 100,
	handler: func(w *ResponseWriter, r *Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			w.SetResponse(codes.NotFound, message.TextPlain, nil)
		}
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
	dialer:                   &net.Dialer{Timeout: time.Second * 3},
	keepalive:                keepalive.New(),
	net:                      "tcp",
	blockwiseSZX:             blockwise.SZX1024,
	blockwiseEnable:          true,
	blockwiseTransferTimeout: time.Second * 3,
}

type dialOptions struct {
	ctx                             context.Context
	maxMessageSize                  int
	heartBeat                       time.Duration
	handler                         HandlerFunc
	errors                          ErrorFunc
	goPool                          GoPoolFunc
	dialer                          *net.Dialer
	keepalive                       *keepalive.KeepAlive
	net                             string
	blockwiseSZX                    blockwise.SZX
	blockwiseEnable                 bool
	blockwiseTransferTimeout        time.Duration
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool
}

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	applyDial(*dialOptions)
}

// ClientConn represents a virtual connection to a conceptual endpoint, to perform COAPs commands.
type ClientConn struct {
	noCopy
	session                 *Session
	observationTokenHandler *HandlerContainer
	observationRequests     *sync.Map
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...DialOption) (*ClientConn, error) {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}

	conn, err := cfg.dialer.DialContext(cfg.ctx, cfg.net, target)
	if err != nil {
		return nil, err
	}

	observatioRequests := &sync.Map{}
	var blockWise *blockwise.BlockWise
	if cfg.blockwiseEnable {
		blockWise = blockwise.NewBlockWise(func(ctx context.Context) blockwise.Message {
			return AcquireMessage(ctx)
		}, func(m blockwise.Message) {
			ReleaseMessage(m.(*Message))
		}, cfg.blockwiseTransferTimeout, cfg.errors, false, func(token message.Token) (blockwise.Message, bool) {
			msg, ok := observatioRequests.Load(token.String())
			if !ok {
				return nil, ok
			}
			return msg.(blockwise.Message), ok
		},
		)
	}

	observationTokenHandler := NewHandlerContainer()

	l := coapNet.NewConn(conn, coapNet.WithHeartBeat(cfg.heartBeat))
	cc := NewClientConn(NewSession(cfg.ctx,
		l,
		NewObservatiomHandler(observationTokenHandler, cfg.handler),
		cfg.maxMessageSize,
		cfg.goPool,
		cfg.blockwiseSZX,
		blockWise,
		cfg.disablePeerTCPSignalMessageCSMs,
		cfg.disableTCPSignalMessageCSM,
	), observationTokenHandler, observatioRequests)

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

// NewClientConn creates connection over session and observation.
func NewClientConn(session *Session, observationTokenHandler *HandlerContainer, observationRequests *sync.Map) *ClientConn {
	return &ClientConn{
		session:                 session,
		observationTokenHandler: observationTokenHandler,
		observationRequests:     observationRequests,
	}
}

// Close closes connection without wait of ends Run function.
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
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// Caller is responsible to release request and response.
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

func newCommonRequest(ctx context.Context, code codes.Code, path string, opts ...message.Option) (*Message, error) {
	token, err := message.GetToken()
	if err != nil {
		return nil, fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireMessage(ctx)
	req.SetCode(code)
	req.SetToken(token)
	req.ResetTo(opts)
	req.SetPath(path)
	return req, nil
}

// NewGetRequest creates get request.
//
// Use ctx to set timeout.
func NewGetRequest(ctx context.Context, path string, opts ...message.Option) (*Message, error) {
	return newCommonRequest(ctx, codes.GET, path, opts...)
}

// Get issues a GET to the specified path.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
func (cc *ClientConn) Get(ctx context.Context, path string, opts ...message.Option) (*Message, error) {
	req, err := NewGetRequest(ctx, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create get request: %w", err)
	}
	defer ReleaseMessage(req)
	return cc.Do(req)
}

// NewPostRequest creates post request.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func NewPostRequest(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.POST, path, opts...)
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
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func (cc *ClientConn) Post(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error) {
	req, err := NewPostRequest(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create post request: %w", err)
	}
	defer ReleaseMessage(req)
	return cc.Do(req)
}

// NewPutRequest creates put request.
//
// Use ctx to set timeout.
//
// If payload is nil then content format is not used.
func NewPutRequest(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.PUT, path, opts...)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.SetContentFormat(contentFormat)
		req.SetPayload(payload)
	}
	return req, nil
}

// Put issues a PUT to the specified path.
//
// Use ctx to set timeout.
//
// An error is returned if by failure to speak COAP (such as a network connectivity problem).
// Any status code doesn't cause an error.
//
// If payload is nil then content format is not used.
func (cc *ClientConn) Put(ctx context.Context, path string, contentFormat message.MediaType, payload io.ReadSeeker, opts ...message.Option) (*Message, error) {
	req, err := NewPutRequest(ctx, path, contentFormat, payload, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create put request: %w", err)
	}
	defer ReleaseMessage(req)
	return cc.Do(req)
}

// Delete deletes the resource identified by the request path.
//
// Use ctx to set timeout.
func (cc *ClientConn) Delete(ctx context.Context, path string, opts ...message.Option) (*Message, error) {
	req, err := newCommonRequest(ctx, codes.DELETE, path, opts...)
	if err != nil {
		return nil, fmt.Errorf("cannot create delete request: %w", err)
	}
	defer ReleaseMessage(req)
	return cc.Do(req)
}

// Context returns the client's context.
//
// If connections was closed context is cancelled.
func (cc *ClientConn) Context() context.Context {
	return cc.session.Context()
}

// Ping issues a PING to the client and waits for PONG reponse.
//
// Use ctx to set timeout.
func (cc *ClientConn) Ping(ctx context.Context) error {
	token, err := message.GetToken()
	if err != nil {
		return fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireMessage(ctx)
	req.SetToken(token)
	req.SetCode(codes.Ping)
	defer ReleaseMessage(req)
	resp, err := cc.Do(req)
	if err != nil {
		return err
	}
	defer ReleaseMessage(resp)
	if resp.Code() == codes.Pong {
		return nil
	}
	return fmt.Errorf("unexpected code(%v)", resp.Code())
}

// Run reads and process requests from a connection, until the connection is not closed.
func (s *ClientConn) Run() (err error) {
	defer func() {
		err1 := s.session.Close()
		if err == nil {
			err = err1
		}
		for _, f := range s.session.onClose {
			f()
		}
	}()
	if !s.session.disableTCPSignalMessageCSM {
		err := s.session.sendCSM()
		if err != nil {
			return err
		}
	}
	for _, f := range s.session.onRun {
		f()
	}
	buffer := bytes.NewBuffer(make([]byte, 0, 1024))
	readBuf := make([]byte, 1024)
	for {
		err = s.processBuffer(buffer)
		if err != nil {
			return err
		}
		readLen, err := s.session.connection.ReadWithContext(s.session.ctx, readBuf)
		if err != nil {
			return err
		}
		if readLen > 0 {
			buffer.Write(readBuf[:readLen])
		}
	}
}

// AddOnClose calls function on close connection event.
func (cc *ClientConn) AddOnClose(f EventFunc) {
	cc.session.AddOnClose(f)
}

func (cc *ClientConn) processBuffer(buffer *bytes.Buffer) error {
	return cc.session.processBuffer(buffer, cc)
}
