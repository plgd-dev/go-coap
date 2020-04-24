package udp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapUDP "github.com/go-ocf/go-coap/v2/message/udp"
	coapNet "github.com/go-ocf/go-coap/v2/net"
)

type EventFunc func()

type Session struct {
	connection *coapNet.UDPConn
	raddr      *net.UDPAddr

	maxMessageSize int
	msgID          uint32
	goPool         GoPoolFunc

	sequence              uint64
	tokenHandlerContainer *HandlerContainer
	midHandlerContainer   *HandlerContainer
	handler               HandlerFunc

	onClose []EventFunc
	onRun   []EventFunc

	cancel  context.CancelFunc
	ctx     context.Context
	wgClose sync.WaitGroup
}

func NewSession(
	ctx context.Context,
	connection *coapNet.UDPConn,
	raddr *net.UDPAddr,
	handler HandlerFunc,
	maxMessageSize int,
	goPool GoPoolFunc,
) *Session {
	ctx, cancel := context.WithCancel(ctx)
	b := make([]byte, 4)
	rand.Read(b)
	msgID := binary.BigEndian.Uint32(b)
	return &Session{
		ctx:                   ctx,
		cancel:                cancel,
		connection:            connection,
		raddr:                 raddr,
		msgID:                 msgID,
		handler:               handler,
		maxMessageSize:        maxMessageSize,
		tokenHandlerContainer: NewHandlerContainer(),
		midHandlerContainer:   NewHandlerContainer(),
		goPool:                goPool,
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

func (s *Session) Handle(w *ResponseWriter, r *Request) {
	if r.Code() == codes.Empty && r.Type() == coapUDP.Confirmable && len(r.Token()) == 0 && len(r.msg.Options) == 0 && len(r.msg.Payload) == 0 {
		s.sendPong(w, r)
		return
	}

	h, err := s.midHandlerContainer.Pop(r.MessageID())
	if err == nil {
		h(w, r)
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

func (s *Session) processBuffer(buffer []byte) error {
	if s.maxMessageSize >= 0 && len(buffer) > s.maxMessageSize {
		return fmt.Errorf("max message size(%v) was exceeded %v", s.maxMessageSize, len(buffer))
	}
	msgRaw := make([]byte, len(buffer))
	copy(msgRaw, buffer)
	req := AcquireRequest(s.ctx)
	_, err := req.Unmarshal(msgRaw)
	if err != nil {
		ReleaseRequest(req)
		return err
	}
	req.sequence = s.Sequence()
	s.goPool(func() error {
		resp := AcquireRequest(s.ctx)
		defer ReleaseRequest(resp)
		resp.SetToken(req.Token())
		w := NewResponseWriter(resp)
		s.Handle(w, req)
		if !req.IsHijacked() {
			ReleaseRequest(req)
		}
		if w.wantWrite() {
			if req.Type() == coapUDP.Confirmable {
				resp.SetType(coapUDP.Acknowledgement)
				resp.SetMessageID(req.MessageID())
			} else {
				resp.SetType(coapUDP.NonConfirmable)
				resp.SetMessageID(s.getMID())
			}
			err := s.WriteRequest(resp)
			if err != nil {
				s.Close()
				return fmt.Errorf("cannot write response: %w", err)
			}
		} else if req.Type() == coapUDP.Confirmable {
			resp.Reset()
			resp.SetCode(codes.Empty)
			resp.SetType(coapUDP.Acknowledgement)
			resp.SetMessageID(req.MessageID())
			err := s.WriteRequest(resp)
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

func (s *Session) WriteRequest(req *Request) error {
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal: %v", err)
	}
	return s.connection.WriteWithContext(req.Context(), s.raddr, data)
}

func (s *Session) sendPong(w *ResponseWriter, r *Request) {
	w.SetCode(codes.Empty)
}
