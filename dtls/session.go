package dtls

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	coapNet "github.com/go-ocf/go-coap/v2/net"
	udpMessage "github.com/go-ocf/go-coap/v2/udp/message"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

type EventFunc func()

type Session struct {
	connection *coapNet.Conn

	maxMessageSize int
	msgID          uint32
	goPool         GoPoolFunc

	sequence              uint64
	tokenHandlerContainer *HandlerContainer
	midHandlerContainer   *HandlerContainer
	handler               HandlerFunc

	blockwiseSZX blockwise.SZX
	blockWise    *blockwise.BlockWise

	onClose []EventFunc
	onRun   []EventFunc

	cancel  context.CancelFunc
	ctx     context.Context
	wgClose sync.WaitGroup
}

func NewSession(
	ctx context.Context,
	connection *coapNet.Conn,
	handler HandlerFunc,
	maxMessageSize int,
	goPool GoPoolFunc,
	blockwiseSZX blockwise.SZX,
	blockWise *blockwise.BlockWise,
) *Session {
	ctx, cancel := context.WithCancel(ctx)
	b := make([]byte, 4)
	rand.Read(b)
	msgID := binary.BigEndian.Uint32(b)

	return &Session{
		ctx:                   ctx,
		cancel:                cancel,
		connection:            connection,
		msgID:                 msgID,
		handler:               handler,
		maxMessageSize:        maxMessageSize,
		tokenHandlerContainer: NewHandlerContainer(),
		midHandlerContainer:   NewHandlerContainer(),
		goPool:                goPool,
		blockWise:             blockWise,
		blockwiseSZX:          blockwiseSZX,
	}
}

func (s *Session) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (s *Session) AddOnClose(f EventFunc) {
	s.onClose = append(s.onClose, f)
}

func (s *Session) AddOnRun(f EventFunc) {
	s.onRun = append(s.onRun, f)
}

func (s *Session) Close() error {
	s.cancel()
	defer s.wgClose.Wait()
	return nil
}

func (s *Session) Sequence() uint64 {
	return atomic.AddUint64(&s.sequence, 1)
}

func (s *Session) Context() context.Context {
	return s.ctx
}

type bwResponseWriter struct {
	w *ResponseWriter
}

func (b *bwResponseWriter) Message() blockwise.Message {
	return b.w.response
}

func (b *bwResponseWriter) SetMessage(m blockwise.Message) {
	pool.ReleaseMessage(b.w.response)
	b.w.response = m.(*pool.Message)
}

func (s *Session) Handle(w *ResponseWriter, r *pool.Message) {
	if r.Code() == codes.Empty && r.Type() == udpMessage.Confirmable && len(r.Token()) == 0 && len(r.Options()) == 0 && r.Body() == nil {
		s.sendPong(w, r)
		return
	}
	h, err := s.midHandlerContainer.Pop(r.MessageID())
	if err == nil {
		h(w, r)
		return
	}
	if s.blockWise != nil {
		bwr := bwResponseWriter{
			w: w,
		}
		s.blockWise.Handle(&bwr, r, s.blockwiseSZX, s.maxMessageSize, func(bw blockwise.ResponseWriter, br blockwise.Message) {
			h, err = s.tokenHandlerContainer.Pop(r.Token())
			w := bw.(*bwResponseWriter).w
			r := br.(*pool.Message)
			if err == nil {
				h(w, r)
				return
			}
			s.handler(w, r)
		})
		return
	}
	h, err = s.tokenHandlerContainer.Pop(r.Token())
	if err == nil {
		h(w, r)
		return
	}
	s.handler(w, r)
}

func (s *Session) TokenHandler() *HandlerContainer {
	return s.tokenHandlerContainer
}

func (s *Session) processBuffer(buffer []byte, cc *ClientConn) error {
	if s.maxMessageSize >= 0 && len(buffer) > s.maxMessageSize {
		return fmt.Errorf("max message size(%v) was exceeded %v", s.maxMessageSize, len(buffer))
	}
	req := pool.AcquireMessage(s.ctx)
	_, err := req.Unmarshal(buffer)
	if err != nil {
		pool.ReleaseMessage(req)
		return err
	}
	req.SetSequence(s.Sequence())
	s.goPool(func() error {
		origResp := pool.AcquireMessage(s.ctx)
		origResp.SetToken(req.Token())
		w := NewResponseWriter(origResp, cc, req.Options())
		typ := req.Type()
		mid := req.MessageID()
		s.Handle(w, req)
		defer pool.ReleaseMessage(w.response)
		if !req.IsHijacked() {
			pool.ReleaseMessage(req)
		}
		if w.response.IsModified() {
			if typ == udpMessage.Confirmable {
				w.response.SetType(udpMessage.Acknowledgement)
				w.response.SetMessageID(mid)
			} else {
				w.response.SetType(udpMessage.NonConfirmable)
				w.response.SetMessageID(s.getMID())
			}
			err := s.WriteRequest(w.response)
			if err != nil {
				s.Close()
				return fmt.Errorf("cannot write response: %w", err)
			}
		} else if typ == udpMessage.Confirmable {
			w.response.Reset()
			w.response.SetCode(codes.Empty)
			w.response.SetType(udpMessage.Acknowledgement)
			w.response.SetMessageID(mid)
			err := s.WriteRequest(w.response)
			if err != nil {
				s.Close()
				return fmt.Errorf("cannot write ack reponse: %w", err)
			}
		}
		return nil
	})
	return nil
}

// GetMID generates a message id for UDP-coap
func (s *Session) getMID() uint16 {
	return uint16(atomic.AddUint32(&s.msgID, 1) % 0xffff)
}

func (s *Session) WriteRequest(req *pool.Message) error {
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal: %v", err)
	}
	return s.connection.WriteWithContext(req.Context(), data)
}

func (s *Session) sendPong(w *ResponseWriter, r *pool.Message) {
	w.SetResponse(codes.Empty, message.TextPlain, nil)
}

// Run reads and process requests from a connection, until the connection is not closed.
func (s *Session) Run(cc *ClientConn) (err error) {
	defer func() {
		err1 := s.Close()
		if err == nil {
			err = err1
		}
		for _, f := range s.onClose {
			f()
		}
	}()
	for _, f := range s.onRun {
		f()
	}
	m := make([]byte, s.maxMessageSize)
	for {
		readBuf := m
		readLen, err := s.connection.ReadWithContext(s.ctx, readBuf)
		if err != nil {
			return err
		}
		readBuf = readBuf[:readLen]
		err = s.processBuffer(readBuf, cc)
		if err != nil {
			return err
		}
	}
}
