package tcp

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapTCP "github.com/go-ocf/go-coap/v2/message/tcp"
	coapNet "github.com/go-ocf/go-coap/v2/net"
)

type EventFunc func()

type Session struct {
	connection *coapNet.Conn

	maxMessageSize                  int
	peerMaxMessageSize              uint32
	peerBlockWiseTranferEnabled     uint32
	disablePeerTCPSignalMessageCSMs bool
	disableTCPSignalMessageCSM      bool
	goPool                          GoPoolFunc

	sequence    uint64
	tokenHander *TokenHandler
	handler     HandlerFunc

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

	disablePeerTCPSignalMessageCSMs bool,
	disableTCPSignalMessageCSM bool,
	goPool GoPoolFunc,
) *Session {
	ctx, cancel := context.WithCancel(ctx)
	return &Session{
		ctx:                             ctx,
		cancel:                          cancel,
		connection:                      connection,
		handler:                         handler,
		maxMessageSize:                  maxMessageSize,
		tokenHander:                     NewTokenHandler(),
		disablePeerTCPSignalMessageCSMs: disablePeerTCPSignalMessageCSMs,
		disableTCPSignalMessageCSM:      disableTCPSignalMessageCSM,
		goPool:                          goPool,
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
	s.handleSignals(w, r)
}

func (s *Session) TokenHandler() *TokenHandler {
	return s.tokenHander
}

func (s *Session) PeerMaxMessageSize() uint32 {
	return atomic.LoadUint32(&s.peerMaxMessageSize)
}

func (s *Session) PeerBlockWiseTransferEnabled() bool {
	return atomic.LoadUint32(&s.peerBlockWiseTranferEnabled) == 1
}

func (s *Session) handleSignals(w *ResponseWriter, r *Request) {
	switch r.Code() {
	case codes.CSM:
		if s.disablePeerTCPSignalMessageCSMs {
			return
		}
		if size, err := r.GetOptionUint32(coapTCP.MaxMessageSize); err == nil {
			atomic.StoreUint32(&s.peerMaxMessageSize, size)
		}
		if r.hasBlockWiseOption() {
			atomic.StoreUint32(&s.peerMaxMessageSize, 1)
		}
		return
	case codes.Ping:
		if r.HasOption(coapTCP.Custody) {
			//TODO
		}
		s.sendPong(w, r)
		return
	case codes.Release:
		if r.HasOption(coapTCP.AlternativeAddress) {
			//TODO
		}
		return
	case codes.Abort:
		if r.HasOption(coapTCP.BadCSMOption) {
			//TODO
		}
		return
	}
	s.tokenHander.Handle(w, r, s.handler)
}

func (s *Session) processBuffer(buffer *bytes.Buffer) error {
	for buffer.Len() > 0 {
		var hdr coapTCP.MessageHeader
		err := hdr.Unmarshal(buffer.Bytes())
		if err == message.ErrShortRead {
			return nil
		}
		if s.maxMessageSize >= 0 && hdr.TotalLen > s.maxMessageSize {
			return fmt.Errorf("max message size(%v) was exceeded %v", s.maxMessageSize, hdr.TotalLen)
		}
		if buffer.Len() < hdr.TotalLen {
			return nil
		}
		msgRaw := make([]byte, hdr.TotalLen)
		n, err := buffer.Read(msgRaw)
		if err != nil {
			return fmt.Errorf("cannot read full: %v", err)
		}
		if n != hdr.TotalLen {
			return fmt.Errorf("invalid data: %v", err)
		}
		req := AcquireRequest(s.ctx)
		_, err = req.UnmarshalWithHeader(hdr, msgRaw[hdr.HeaderLen:])
		if err != nil {
			ReleaseRequest(req)
			return fmt.Errorf("cannot unmarshal with header: %v", err)
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
				err := s.WriteRequest(resp)
				if err != nil {
					s.cancel()
					return fmt.Errorf("cannot write request: %v", err)
				}
			}
			return nil
		})
	}
	return nil
}

func (s *Session) WriteRequest(req *Request) error {
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal: %v", err)
	}
	return s.connection.WriteWithContext(req.Context(), data)
}

func (s *Session) sendCSM() error {
	token, err := message.GetToken()
	if err != nil {
		return fmt.Errorf("cannot get token: %w", err)
	}
	req := AcquireRequest(s.ctx)
	defer ReleaseRequest(req)
	req.SetCode(codes.CSM)
	req.SetToken(token)
	return s.WriteRequest(req)
}

func (s *Session) sendPong(w *ResponseWriter, r *Request) {
	w.SetCode(codes.Pong)
}

func (s *Session) Run() (err error) {
	defer func() {
		err1 := s.connection.Close()
		if err == nil {
			err = err1
		}
		for _, f := range s.onClose {
			f()
		}
	}()
	s.wgClose.Add(1)
	defer s.wgClose.Done()
	if !s.disableTCPSignalMessageCSM {
		err := s.sendCSM()
		if err != nil {
			return err
		}
	}
	for _, f := range s.onRun {
		f()
	}
	buffer := bytes.NewBuffer(make([]byte, 0, 1024))
	readBuf := make([]byte, 1024)
	for {
		err = s.processBuffer(buffer)
		if err != nil {
			return err
		}
		readLen, err := s.connection.ReadWithContext(s.ctx, readBuf)
		if err != nil {
			return err
		}
		if readLen > 0 {
			buffer.Write(readBuf[:readLen])
		}
	}
}
