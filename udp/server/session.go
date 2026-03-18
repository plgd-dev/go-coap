package server

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

type EventFunc = func()

type Session struct {
	onClose []EventFunc

	ctx atomic.Pointer[context.Context]

	doneCtx    context.Context
	connection *coapNet.UDPConn
	doneCancel context.CancelFunc

	cancel        context.CancelFunc
	raddr         *net.UDPAddr
	originalDstIP net.IP // Stores the original destination IP from received packets (public IP on Fly.io)

	mutex          sync.Mutex
	maxMessageSize uint32
	mtu            uint16

	closeSocket bool
}

func NewSession(
	ctx context.Context,
	doneCtx context.Context,
	connection *coapNet.UDPConn,
	raddr *net.UDPAddr,
	originalDstIP net.IP,
	maxMessageSize uint32,
	mtu uint16,
	closeSocket bool,
) *Session {
	ctx, cancel := context.WithCancel(ctx)

	doneCtx, doneCancel := context.WithCancel(doneCtx)
	s := &Session{
		cancel:         cancel,
		connection:     connection,
		raddr:          raddr,
		originalDstIP:  originalDstIP,
		maxMessageSize: maxMessageSize,
		mtu:            mtu,
		closeSocket:    closeSocket,
		doneCtx:        doneCtx,
		doneCancel:     doneCancel,
	}
	s.ctx.Store(&ctx)
	return s
}

// SetContextValue stores the value associated with key to context of connection.
func (s *Session) SetContextValue(key interface{}, val interface{}) {
	ctx := context.WithValue(s.Context(), key, val)
	s.ctx.Store(&ctx)
}

// Done signalizes that connection is not more processed.
func (s *Session) Done() <-chan struct{} {
	return s.doneCtx.Done()
}

func (s *Session) AddOnClose(f EventFunc) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.onClose = append(s.onClose, f)
}

func (s *Session) popOnClose() []EventFunc {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	tmp := s.onClose
	s.onClose = nil
	return tmp
}

func (s *Session) shutdown() {
	defer s.doneCancel()
	for _, f := range s.popOnClose() {
		f()
	}
}

func (s *Session) Close() error {
	s.cancel()
	if s.closeSocket {
		return s.connection.Close()
	}
	return nil
}

func (s *Session) Context() context.Context {
	return *s.ctx.Load()
}

func (s *Session) WriteMessage(req *pool.Message) error {
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return fmt.Errorf("cannot marshal: %w", err)
	}

	// Get or create the control message with the correct source address
	cm := req.ControlMessage()
	if cm == nil {
		cm = &coapNet.ControlMessage{}
	}
	// Set the source address to the original destination IP (public IP that client sent to)
	// This ensures responses are sent from the same IP the client sent to, which is critical
	// for environments like Fly.io where packets arrive at a public IP but the socket is
	// bound to an internal private address.
	if s.originalDstIP != nil && cm.Src == nil {
		cm.Src = s.originalDstIP
	}

	return s.connection.WriteWithOptions(data, coapNet.WithContext(req.Context()), coapNet.WithRemoteAddr(s.raddr), coapNet.WithControlMessage(cm))
}

// WriteMulticastMessage sends multicast to the remote multicast address.
// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
// Via opts you can specify the network interface, source IP address, and hop limit.
func (s *Session) WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, opts ...coapNet.MulticastOption) error {
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return fmt.Errorf("cannot marshal: %w", err)
	}

	return s.connection.WriteMulticast(req.Context(), address, data, opts...)
}

func (s *Session) Run(cc *client.Conn) (err error) {
	defer func() {
		err1 := s.Close()
		if err == nil {
			err = err1
		}
		s.shutdown()
	}()
	m := make([]byte, s.mtu)
	for {
		buf := m
		var cm *coapNet.ControlMessage
		n, err := s.connection.ReadWithOptions(buf, coapNet.WithContext(s.Context()), coapNet.WithGetControlMessage(&cm))
		if err != nil {
			return err
		}
		buf = buf[:n]
		err = cc.Process(cm, buf)
		if err != nil {
			return err
		}
	}
}

func (s *Session) MaxMessageSize() uint32 {
	return s.maxMessageSize
}

func (s *Session) RemoteAddr() net.Addr {
	return s.raddr
}

func (s *Session) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// NetConn returns the underlying connection that is wrapped by s. The Conn returned is shared by all invocations of NetConn, so do not modify it.
func (s *Session) NetConn() net.Conn {
	return s.connection.NetConn()
}
