package coap

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"net"

	"github.com/go-ocf/go-coap/codes"
	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
)

type sessionDTLS struct {
	*sessionBase
	connection       *coapNet.Conn
	peerCertificates []*x509.Certificate
}

// newSessionDTLS create new session for DTLS connection
func newSessionDTLS(connection *coapNet.Conn, srv *Server) (networkSession, error) {
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

	s := sessionDTLS{
		sessionBase: newBaseSession(BlockWiseTransfer, BlockWiseTransferSzx, srv),
		connection:  connection,
	}

	dtlsConn := connection.Connection().(*dtls.Conn)
	cert := dtlsConn.RemoteCertificate()
	if len(cert) > 0 {
		flatCerts := bytes.Join(cert, nil)
		peerCertificates, err := x509.ParseCertificates(flatCerts)
		if err != nil {
			return nil, err
		}

		s.peerCertificates = peerCertificates
	}

	return &s, nil
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionDTLS) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (s *sessionDTLS) RemoteAddr() net.Addr {
	return s.connection.RemoteAddr()
}

// PeerCertificates implements the networkSession.PeerCertificates method.
func (s *sessionDTLS) PeerCertificates() []*x509.Certificate {
	return s.peerCertificates
}

// BlockWiseTransferEnabled
func (s *sessionDTLS) blockWiseEnabled() bool {
	return s.blockWiseTransfer
}

func (s *sessionDTLS) blockWiseIsValid(szx BlockWiseSzx) bool {
	return szx <= BlockWiseSzx1024
}

// Ping send ping over udp(unicast) and wait for response.
func (s *sessionDTLS) PingWithContext(ctx context.Context) error {
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

func (s *sessionDTLS) closeWithError(err error) error {
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
func (s *sessionDTLS) Close() error {
	return s.closeWithError(nil)
}

// NewMessage Create message for response
func (s *sessionDTLS) NewMessage(p MessageParams) Message {
	return NewDgramMessage(p)
}

// Close implements the networkSession.Close method
func (s *sessionDTLS) IsTCP() bool {
	return false
}

func (s *sessionDTLS) ExchangeWithContext(ctx context.Context, req Message) (Message, error) {
	return s.exchangeWithContext(ctx, req, s.WriteMsgWithContext)
}

// Write implements the networkSession.Write method.
func (s *sessionDTLS) WriteMsgWithContext(ctx context.Context, req Message) error {
	buffer := bytes.NewBuffer(make([]byte, 0, 1500))
	err := req.MarshalBinary(buffer)
	if err != nil {
		return fmt.Errorf("cannot write msg to tcp connection %v", err)
	}
	return s.connection.WriteWithContext(ctx, buffer.Bytes())
}

func (s *sessionDTLS) sendPong(w ResponseWriter, r *Request) error {
	resp := r.Client.NewMessage(MessageParams{
		Type:      Reset,
		Code:      codes.Empty,
		MessageID: r.Msg.MessageID(),
	})
	return w.WriteMsgWithContext(r.Ctx, resp)
}

func (s *sessionDTLS) handleSignals(w ResponseWriter, r *Request) bool {
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
