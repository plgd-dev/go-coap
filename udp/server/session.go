package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/onclose"
	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	netClient "github.com/plgd-dev/go-coap/v3/net/client"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

type EventFunc = func()

type Session struct {
	onClose onclose.Callbacks

	ctx atomic.Pointer[context.Context]

	doneCtx    context.Context
	connection *coapNet.UDPConn
	doneCancel context.CancelFunc

	cancel context.CancelFunc
	raddr  *net.UDPAddr

	maxMessageSize uint32
	mtu            uint16

	closeSocket  bool
	errors       client.ErrorFunc
	writeMu      sync.RWMutex
	writeTimeout time.Duration
}

// NewSession creates a UDP session. Optional error handlers receive recoverable
// datagram errors; otherwise the default client error handler is used.
func NewSession(
	ctx context.Context,
	doneCtx context.Context,
	connection *coapNet.UDPConn,
	raddr *net.UDPAddr,
	maxMessageSize uint32,
	mtu uint16,
	closeSocket bool,
	errorHandlers ...client.ErrorFunc,
) *Session {
	return newSession(ctx, doneCtx, connection, raddr, maxMessageSize, mtu, closeSocket, 0, errorHandlers...)
}

func newSession(
	ctx context.Context,
	doneCtx context.Context,
	connection *coapNet.UDPConn,
	raddr *net.UDPAddr,
	maxMessageSize uint32,
	mtu uint16,
	closeSocket bool,
	writeTimeout time.Duration,
	errorHandlers ...client.ErrorFunc,
) *Session {
	ctx, cancel := context.WithCancel(ctx)

	doneCtx, doneCancel := context.WithCancel(doneCtx)
	s := &Session{
		cancel:         cancel,
		connection:     connection,
		raddr:          raddr,
		maxMessageSize: maxMessageSize,
		mtu:            mtu,
		closeSocket:    closeSocket,
		doneCtx:        doneCtx,
		doneCancel:     doneCancel,
		errors:         client.DefaultConfig.Errors,
		writeTimeout:   writeTimeout,
	}
	if len(errorHandlers) > 0 {
		s.errors = func(err error) {
			for _, handler := range errorHandlers {
				if handler != nil {
					handler(err)
				}
			}
		}
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
	s.onClose.Add(f)
}

func (s *Session) shutdown() {
	defer s.doneCancel()
	s.onClose.Close()
}

// Close cancels the session and blocks until in-flight writes have drained.
// It must not be called while holding the session write lock.
func (s *Session) Close() error {
	s.cancel()
	var err error
	if s.closeSocket {
		err = s.connection.Close()
	}
	// Drain in-flight writes before the server releases this peer's MID sequence.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return err
}

func (s *Session) Context() context.Context {
	return *s.ctx.Load()
}

func (s *Session) WriteMessage(req *pool.Message) error {
	if err := s.Context().Err(); err != nil {
		return &netClient.RequestNotSentError{Err: errors.Join(err, net.ErrClosed)}
	}
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return &netClient.RequestNotSentError{Err: fmt.Errorf("cannot marshal: %w", err)}
	}
	ctx, cancel := writecontext.New(req.Context(), s.Context(), s.writeTimeout)
	defer cancel()
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	if err := s.Context().Err(); err != nil {
		return &netClient.RequestNotSentError{Err: errors.Join(err, net.ErrClosed)}
	}
	return s.connection.WriteWithOptions(data, coapNet.WithContext(ctx), coapNet.WithRemoteAddr(s.raddr), coapNet.WithControlMessage(req.ControlMessage()))
}

// WriteMulticastMessage sends multicast to the remote multicast address.
// By default it is sent over all network interfaces and all compatible source IP addresses with hop limit 1.
// Via opts you can specify the network interface, source IP address, and hop limit.
func (s *Session) WriteMulticastMessage(req *pool.Message, address *net.UDPAddr, opts ...coapNet.MulticastOption) error {
	multicastOpts := coapNet.DefaultMulticastOptions()
	for _, opt := range opts {
		multicastOpts.Apply(opt)
	}
	type interfaceFailure struct {
		iface *net.Interface
		err   error
	}
	var failures []interfaceFailure
	if multicastOpts.InterfaceError != nil {
		opts = append(append([]coapNet.MulticastOption(nil), opts...), coapNet.WithMulticastInterfaceError(func(iface *net.Interface, err error) {
			failures = append(failures, interfaceFailure{iface: iface, err: err})
		}))
	}
	writeErr := func() error {
		if s.Context().Err() != nil {
			return net.ErrClosed
		}
		data, err := req.MarshalWithEncoder(coder.DefaultCoder)
		if err != nil {
			return fmt.Errorf("cannot marshal: %w", err)
		}
		ctx, cancel := writecontext.New(req.Context(), s.Context(), s.writeTimeout)
		defer cancel()
		s.writeMu.RLock()
		defer s.writeMu.RUnlock()
		if s.Context().Err() != nil {
			return net.ErrClosed
		}
		return s.connection.WriteMulticast(ctx, address, data, opts...)
	}()
	for _, failure := range failures {
		multicastOpts.InterfaceError(failure.iface, failure.err)
	}
	return writeErr
}

// rejectMalformedConfirmable replies with RST to a malformed confirmable message
// whose header could be parsed (RFC 7252 section 4.2). Unknown versions are silently ignored (section 3).
func (s *Session) rejectMalformedConfirmable(cm *coapNet.ControlMessage, malformedErr *client.MalformedMessageError) {
	if malformedErr.InvalidVersion {
		return
	}
	s.errors(fmt.Errorf("%v: dropping malformed packet: %w", s.raddr, malformedErr))
	if !malformedErr.HasHeader || malformedErr.Type != message.Confirmable {
		return
	}
	rst, err := makeResetDatagram(malformedErr.MessageID)
	if err != nil {
		return
	}
	ctx, cancel := writecontext.New(context.Background(), s.Context(), s.writeTimeout)
	defer cancel()
	writeOpts := []coapNet.UDPWriteOption{coapNet.WithContext(ctx), coapNet.WithRemoteAddr(s.raddr)}
	if cm != nil {
		writeOpts = append(writeOpts, coapNet.WithControlMessage(&coapNet.ControlMessage{
			IfIndex: cm.GetIfIndex(),
			Src:     cm.Dst,
		}))
	}
	if err := s.connection.WriteWithOptions(rst, writeOpts...); err != nil {
		s.errors(fmt.Errorf("%v: cannot send reset for malformed confirmable packet: %w", s.raddr, err))
	}
}

func (s *Session) Run(cc *client.Conn) (err error) {
	defer func() {
		err1 := s.Close()
		if err == nil {
			err = err1
		}
		s.shutdown()
	}()
	// One extra byte so that a datagram larger than the MTU is detected
	// instead of being silently truncated and processed.
	m := make([]byte, int(s.mtu)+1)
	for {
		buf := m
		var cm *coapNet.ControlMessage
		n, err := s.connection.ReadWithOptions(buf, coapNet.WithContext(s.Context()), coapNet.WithGetControlMessage(&cm))
		if err != nil {
			if coapNet.IsMessageSizeError(err) {
				s.errors(fmt.Errorf("cannot read oversized datagram: %w", err))
				continue
			}
			return err
		}
		if n > int(s.mtu) {
			s.errors(fmt.Errorf("datagram exceeds MTU(%v)", s.mtu))
			continue
		}
		if int64(n) > int64(s.maxMessageSize) {
			s.errors(fmt.Errorf("max message size(%v) was exceeded %v", s.maxMessageSize, n))
			continue
		}
		buf = buf[:n]
		err = cc.Process(cm, buf)
		if err != nil {
			var malformedErr *client.MalformedMessageError
			if !errors.As(err, &malformedErr) {
				return err
			}
			// Malformed datagram must not tear down the session.
			s.rejectMalformedConfirmable(cm, malformedErr)
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
