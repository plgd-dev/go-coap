package coap

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"net"

	"github.com/go-ocf/go-coap/codes"
	coapNet "github.com/go-ocf/go-coap/net"
)

type connUDP interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Close() error
	ReadWithContext(ctx context.Context, buffer []byte) (int, *coapNet.ConnUDPContext, error)
	WriteWithContext(ctx context.Context, udpCtx *coapNet.ConnUDPContext, buffer []byte) error
}

type sessionUDP struct {
	*sessionBase
	connection     connUDP
	sessionUDPData *coapNet.ConnUDPContext // oob data to get egress interface right
}

// NewSessionUDP create new session for UDP connection
func newSessionUDP(connection connUDP, srv *Server, sessionUDPData *coapNet.ConnUDPContext) (networkSession, error) {
	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	if srv.BlockWiseTransfer != nil {
		BlockWiseTransfer = *srv.BlockWiseTransfer
	}
	if srv.BlockWiseTransferSzx != nil {
		BlockWiseTransferSzx = *srv.BlockWiseTransferSzx
	}

	if BlockWiseTransfer && BlockWiseTransferSzx == BlockWiseSzxBERT {
		return nil, ErrInvalidBlockWiseSzx
	}

	s := &sessionUDP{
		sessionBase:    newBaseSession(BlockWiseTransfer, BlockWiseTransferSzx, srv),
		connection:     connection,
		sessionUDPData: sessionUDPData,
	}
	return s, nil
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionUDP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (s *sessionUDP) RemoteAddr() net.Addr {
	return s.sessionUDPData.RemoteAddr()
}

// PeerCertificates implements the networkSession.PeerCertificates method.
func (s *sessionUDP) PeerCertificates() []*x509.Certificate {
	return nil
}

// BlockWiseTransferEnabled
func (s *sessionUDP) blockWiseEnabled() bool {
	return s.blockWiseTransfer
}

func (s *sessionUDP) blockWiseIsValid(szx BlockWiseSzx) bool {
	return szx <= BlockWiseSzx1024
}

func (s *sessionUDP) closeWithError(err error) error {
	if s.sessionBase.Close() == nil {
		s.srv.sessionUDPMapLock.Lock()
		delete(s.srv.sessionUDPMap, s.sessionUDPData.Key())
		s.srv.sessionUDPMapLock.Unlock()
		c := ClientConn{commander: &ClientCommander{s}}
		s.srv.NotifySessionEndFunc(&c, err)
	}

	return nil
}

// Ping send ping over udp(unicast) and wait for response.
func (s *sessionUDP) PingWithContext(ctx context.Context) error {
	//provoking to get a reset message - "CoAP ping" in RFC-7252
	//https://tools.ietf.org/html/rfc7252#section-4.2
	//https://tools.ietf.org/html/rfc7252#section-4.3
	//https://tools.ietf.org/html/rfc7252#section-1.2 "Reset Message"
	// BUG of iotivity: https://jira.iotivity.org/browse/IOT-3149
	req := s.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      codes.Empty,
		MessageID: GenerateMessageID(),
	})
	resp, err := s.ExchangeWithContext(ctx, req)
	if err != nil {
		return err
	}
	if resp.Type() == Reset || resp.Type() == Acknowledgement {
		return nil
	}
	return ErrInvalidResponse
}

// Close implements the networkSession.Close method
func (s *sessionUDP) Close() error {
	return s.closeWithError(nil)
}

// NewMessage Create message for response
func (s *sessionUDP) NewMessage(p MessageParams) Message {
	return NewDgramMessage(p)
}

// Close implements the networkSession.Close method
func (s *sessionUDP) IsTCP() bool {
	return false
}

func (s *sessionUDP) ExchangeWithContext(ctx context.Context, req Message) (Message, error) {
	return s.exchangeWithContext(ctx, req, s.WriteMsgWithContext)
}

func (s *sessionUDP) WriteMsgWithContext(ctx context.Context, req Message) error {
	buffer := bytes.NewBuffer(make([]byte, 0, 1500))
	err := req.MarshalBinary(buffer)
	if err != nil {
		return fmt.Errorf("cannot write msg to udp connection %v", err)
	}
	return s.connection.WriteWithContext(ctx, s.sessionUDPData, buffer.Bytes())
}

func (s *sessionUDP) sendPong(w ResponseWriter, r *Request) error {
	resp := r.Client.NewMessage(MessageParams{
		Type:      Acknowledgement,
		Code:      codes.Empty,
		MessageID: r.Msg.MessageID(),
	})
	return w.WriteMsgWithContext(r.Ctx, resp)
}

func (s *sessionUDP) handleSignals(w ResponseWriter, r *Request) bool {
	switch r.Msg.Code() {
	// handle of udp ping
	case codes.Empty:
		if r.Msg.Type() == Confirmable && r.Msg.AllOptions().Len() == 0 && (r.Msg.Payload() == nil || len(r.Msg.Payload()) == 0) {
			s.sendPong(w, r)
			return true
		}
	}
	return false
}
