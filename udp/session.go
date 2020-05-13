package udp

import (
	"context"
	"fmt"
	"net"
	"sync"

	coapNet "github.com/go-ocf/go-coap/v2/net"
	"github.com/go-ocf/go-coap/v2/udp/client"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

type EventFunc = func()

type Session struct {
	connection     *coapNet.UDPConn
	raddr          *net.UDPAddr
	maxMessageSize int

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
	maxMessageSize int,
) *Session {
	ctx, cancel := context.WithCancel(ctx)
	return &Session{
		ctx:            ctx,
		cancel:         cancel,
		connection:     connection,
		raddr:          raddr,
		maxMessageSize: maxMessageSize,
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

func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) WriteMessage(req *pool.Message) error {
	data, err := req.Marshal()
	if err != nil {
		return fmt.Errorf("cannot marshal: %v", err)
	}
	return s.connection.WriteWithContext(req.Context(), s.raddr, data)
}

func (s *Session) Run(cc *client.ClientConn) error {
	m := make([]byte, s.maxMessageSize)
	for {
		buf := m
		n, _, err := s.connection.ReadWithContext(s.ctx, buf)
		if err != nil {
			s.Close()
			return err
		}
		buf = buf[:n]
		err = cc.Process(buf)
		if err != nil {
			s.Close()
			return err
		}
	}
}

func (s *Session) MaxMessageSize() int {
	return s.maxMessageSize
}

func (s *Session) RemoteAddr() net.Addr {
	return s.raddr
}
