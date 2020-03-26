package coap

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/go-ocf/go-coap/codes"
	coapNet "github.com/go-ocf/go-coap/net"
)

type sessionTCP struct {
	*sessionBase
	connection *coapNet.Conn

	peerBlockWiseTransfer           uint32
	peerMaxMessageSize              uint32
	disablePeerTCPSignalMessageCSMs bool
}

// newSessionTCP create new session for TCP connection
func newSessionTCP(connection *coapNet.Conn, srv *Server) (networkSession, error) {
	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockWiseSzxBERT
	if srv.BlockWiseTransfer != nil {
		BlockWiseTransfer = *srv.BlockWiseTransfer
	}
	if srv.BlockWiseTransferSzx != nil {
		BlockWiseTransferSzx = *srv.BlockWiseTransferSzx
	}
	s := &sessionTCP{
		peerMaxMessageSize:              uint32(srv.MaxMessageSize),
		disablePeerTCPSignalMessageCSMs: srv.DisablePeerTCPSignalMessageCSMs,
		connection:                      connection,
		sessionBase:                     newBaseSession(BlockWiseTransfer, BlockWiseTransferSzx, srv),
	}

	if !s.srv.DisableTCPSignalMessageCSM {
		if err := s.sendCSM(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionTCP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (s *sessionTCP) RemoteAddr() net.Addr {
	return s.connection.RemoteAddr()
}

// PeerCertificates implements the networkSession.PeerCertificates method.
func (s *sessionTCP) PeerCertificates() []*x509.Certificate {
	return nil
}

func (s *sessionTCP) blockWiseEnabled() bool {
	return s.blockWiseTransfer /*&& atomic.LoadUint32(&s.peerBlockWiseTransfer) != 0*/
}

func (s *sessionTCP) blockWiseMaxPayloadSize(peer BlockWiseSzx) (int, BlockWiseSzx) {
	szx := s.blockWiseSzx()
	if szx == BlockWiseSzxBERT && peer == BlockWiseSzxBERT {
		m := atomic.LoadUint32(&s.peerMaxMessageSize)
		if m == 0 {
			m = uint32(s.srv.MaxMessageSize)
		}
		return int(m - (m % 1024)), BlockWiseSzxBERT
	}
	return s.sessionBase.blockWiseMaxPayloadSize(peer)
}

func (s *sessionTCP) blockWiseIsValid(szx BlockWiseSzx) bool {
	return true
}

func (s *sessionTCP) PingWithContext(ctx context.Context) error {
	token, err := GenerateToken()
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  codes.Ping,
		Token: []byte(token),
	})
	resp, err := s.ExchangeWithContext(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code() == codes.Pong {
		return nil
	}
	return ErrInvalidResponse
}

func (s *sessionTCP) closeWithError(err error) error {
	if s.sessionBase.Close() == nil {
		c := ClientConn{commander: &ClientCommander{s}}
		s.srv.NotifySessionEndFunc(&c, err)
		e := s.connection.Close()
		if e == nil {
			e = err
		}
		return e
	}
	return nil
}

// Close implements the networkSession.Close method
func (s *sessionTCP) Close() error {
	return s.closeWithError(nil)
}

// NewMessage Create message for response
func (s *sessionTCP) NewMessage(p MessageParams) Message {
	return NewTcpMessage(p)
}

// Close implements the networkSession.Close method
func (s *sessionTCP) IsTCP() bool {
	return true
}

func (s *sessionTCP) ExchangeWithContext(ctx context.Context, req Message) (Message, error) {
	if req.Token() == nil {
		return nil, ErrTokenNotExist
	}
	return s.exchangeWithContext(ctx, req, s.WriteMsgWithContext)
}

func (s *sessionTCP) validateMessageSize(msg Message) error {
	size, err := msg.ToBytesLength()
	if err != nil {
		return err
	}

	max := atomic.LoadUint32(&s.peerMaxMessageSize)
	if max != 0 && uint32(size) > max {
		return ErrMaxMessageSizeLimitExceeded
	}

	return nil
}

// Write implements the networkSession.Write method.
func (s *sessionTCP) WriteMsgWithContext(ctx context.Context, req Message) error {
	if err := s.validateMessageSize(req); err != nil {
		return err
	}
	buffer := bytes.NewBuffer(make([]byte, 0, 1500))
	err := req.MarshalBinary(buffer)
	if err != nil {
		return fmt.Errorf("cannot write msg to tcp connection %v", err)
	}
	return s.connection.WriteWithContext(ctx, buffer.Bytes())
}

func (s *sessionTCP) sendCSM() error {
	token, err := GenerateToken()
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  codes.CSM,
		Token: []byte(token),
	})
	if s.srv.MaxMessageSize != 0 {
		req.AddOption(MaxMessageSize, uint32(s.srv.MaxMessageSize))
	}
	if s.blockWiseEnabled() {
		req.AddOption(BlockWiseTransfer, []byte{})
	}
	return s.WriteMsgWithContext(context.Background(), req)
}

func (s *sessionTCP) setPeerMaxMessageSize(val uint32) {
	atomic.StoreUint32(&s.peerMaxMessageSize, val)
}

func (s *sessionTCP) setPeerBlockWiseTransfer(val bool) {
	v := uint32(0)
	if val {
		v = 1
	}
	atomic.StoreUint32(&s.peerBlockWiseTransfer, v)
}

func (s *sessionTCP) sendPong(w ResponseWriter, r *Request) error {
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  codes.Pong,
		Token: r.Msg.Token(),
	})
	return w.WriteMsgWithContext(r.Ctx, req)
}

func (s *sessionTCP) handleSignals(w ResponseWriter, r *Request) bool {
	switch r.Msg.Code() {
	case codes.CSM:
		if s.disablePeerTCPSignalMessageCSMs {
			return true
		}
		maxmsgsize := uint32(maxMessageSize)
		if size, ok := r.Msg.Option(MaxMessageSize).(uint32); ok {
			s.setPeerMaxMessageSize(size)
			maxmsgsize = size
		}
		if r.Msg.Option(BlockWiseTransfer) != nil {
			s.setPeerBlockWiseTransfer(true)
			startIter := s.blockWiseSzx()
			if startIter == BlockWiseSzxBERT {
				if szxToBytes[BlockWiseSzx1024] < int(maxmsgsize) {
					s.setBlockWiseSzx(BlockWiseSzxBERT)
					return true
				}
				startIter = BlockWiseSzx512
			}
			for i := startIter; i > BlockWiseSzx16; i-- {
				if szxToBytes[i] < int(maxmsgsize) {
					s.setBlockWiseSzx(i)
					return true
				}
			}
			s.setBlockWiseSzx(BlockWiseSzx16)
		}

		return true
	case codes.Ping:
		if r.Msg.Option(Custody) != nil {
			//TODO
		}
		s.sendPong(w, r)
		return true
	case codes.Release:
		if _, ok := r.Msg.Option(AlternativeAddress).(string); ok {
			//TODO
		}
		return true
	case codes.Abort:
		if _, ok := r.Msg.Option(BadCSMOption).(uint32); ok {
			//TODO
		}
		return true
	}
	return false
}
