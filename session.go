package coap

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// A Session interface is used by an COAP handler to
// server data in session.
type Session interface {
	// LocalAddr returns the net.Addr of the server
	LocalAddr() net.Addr
	// RemoteAddr returns the net.Addr of the client that sent the current request.
	RemoteAddr() net.Addr
	// WriteMsg writes a reply back to the client.
	WriteMsg(resp Message, timeout time.Duration) error
	// Close closes the connection.
	Close() error
	// Return type of network
	IsTCP() bool
	// Create message for response via writter
	NewMessage(params MessageParams) Message
	// Exchange writes message and wait for response - paired by token and msgid
	// it is safe to use in goroutines
	Exchange(req Message, timeout time.Duration) (Message, error)
	// Send ping to peer and wait for pong
	Ping(timeout time.Duration) error

	// handlePairMsg Message was handled by pair
	handlePairMsg(req Message) bool

	// handleSignals Message below to signals
	handleSignals(req Message) bool

	// sendPong create pong by m and send it
	sendPong(m Message) error

	// close session with error
	closeWithError(err error) error
}

// NewSessionUDP create new session for UDP connection
func NewSessionUDP(connection Conn, srv *Server, sessionUDPData *SessionUDPData) (Session, error) {
	s := &sessionUDP{sessionBase: sessionBase{srv: srv, mapPairs: make(map[[8]byte](*sessionResp)), connection: connection}, sessionUDPData: sessionUDPData, mapMsgIdPairs: make(map[uint16](*sessionResp))}
	return s, nil
}

// NewSessionTCP create new session for TCP connection
func NewSessionTCP(connection Conn, srv *Server) (Session, error) {
	s := &sessionTCP{sessionBase: sessionBase{srv: srv, mapPairs: make(map[[8]byte](*sessionResp)), connection: connection}}
	if err := s.sendCSM(); err != nil {
		return nil, err
	}
	return s, nil
}

type sessionResp struct {
	ch chan Message // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv        *Server
	connection Conn

	mapPairs     map[[MaxTokenSize]byte]*sessionResp //storage of channel Message
	mapPairsLock sync.Mutex                          //to sync add remove token
}

type sessionUDP struct {
	sessionBase
	sessionUDPData    *SessionUDPData         // oob data to get egress interface right
	mapMsgIdPairs     map[uint16]*sessionResp //storage of channel Message
	mapMsgIdPairsLock sync.Mutex              //to sync add remove token
}

type sessionTCP struct {
	sessionBase
	peerMaxMessageSize    uint32 // SCM from peer set peerMaxMessageSize
	peerBlockWiseTransfer uint32 // SCM from peer inform us that it supports blockwise (+ BERT when peerMaxMessageSize > 1152 )
}

// LocalAddr implements the Session.LocalAddr method.
func (s *sessionUDP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// LocalAddr implements the Session.LocalAddr method.
func (s *sessionTCP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the Session.RemoteAddr method.
func (s *sessionUDP) RemoteAddr() net.Addr {
	return s.sessionUDPData.RemoteAddr()
}

// RemoteAddr implements the Session.RemoteAddr method.
func (s *sessionTCP) RemoteAddr() net.Addr {
	return s.connection.RemoteAddr()
}

func (s *sessionUDP) closeWithError(err error) error {
	s.srv.sessionUDPMapLock.Lock()
	delete(s.srv.sessionUDPMap, s.sessionUDPData.Key())
	s.srv.sessionUDPMapLock.Unlock()

	s.srv.NotifySessionEndFunc(s, err)

	return err
}

// Ping send ping over udp(unicast) and wait for response.
func (s *sessionUDP) Ping(timeout time.Duration) error {
	//provoking to get a reset message - "CoAP ping" in RFC-7252
	//https://tools.ietf.org/html/rfc7252#section-4.2
	//https://tools.ietf.org/html/rfc7252#section-4.3
	//https://tools.ietf.org/html/rfc7252#section-1.2 "Reset Message"
	// BUG of iotivity: https://jira.iotivity.org/browse/IOT-3149
	req := s.NewMessage(MessageParams{
		Type:      Confirmable,
		Code:      Empty,
		MessageID: GenerateMessageId(),
	})
	resp, err := s.Exchange(req, timeout)
	if err != nil {
		return err
	}
	if resp.Type() == Reset {
		return nil
	}
	return ErrInvalidResponse
}

func (s *sessionTCP) Ping(timeout time.Duration) error {
	token, err := GenerateToken(MaxTokenSize)
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Ping,
		Token: []byte(token),
	})
	resp, err := s.Exchange(req, timeout)
	if err != nil {
		return err
	}
	if resp.Code() == Pong {
		return nil
	}
	return ErrInvalidResponse
}

// Close implements the Session.Close method
func (s *sessionUDP) Close() error {
	return s.closeWithError(nil)
}

func (s *sessionTCP) closeWithError(err error) error {
	if s.connection != nil {
		s.srv.NotifySessionEndFunc(s, err)
		e := s.connection.Close()
		//s.connection = nil
		if e == nil {
			e = err
		}
		return e
	}
	return err
}

// Close implements the Session.Close method
func (s *sessionTCP) Close() error {
	return s.closeWithError(nil)
}

// NewMessage Create message for response
func (s *sessionUDP) NewMessage(p MessageParams) Message {
	return NewDgramMessage(p)
}

// NewMessage Create message for response
func (s *sessionTCP) NewMessage(p MessageParams) Message {
	return NewTcpMessage(p)
}

// Close implements the Session.Close method
func (s *sessionUDP) IsTCP() bool {
	return false
}

// Close implements the Session.Close method
func (s *sessionTCP) IsTCP() bool {
	return true
}

func (s *sessionBase) exchangeFunc(req Message, timeout time.Duration, pairChan *sessionResp, write func(msg Message, timeout time.Duration) error) (Message, error) {
	var pairToken [MaxTokenSize]byte
	if req.Token() != nil {
		copy(pairToken[:], req.Token())
		s.mapPairsLock.Lock()
		for s.mapPairs[pairToken] != nil {
			return nil, ErrTokenAlreadyExist
		}

		s.mapPairs[pairToken] = pairChan
		s.mapPairsLock.Unlock()
	}

	defer func() {
		if req.Token() != nil {
			s.mapPairsLock.Lock()
			s.mapPairs[pairToken] = nil
			s.mapPairsLock.Unlock()
		}
	}()

	err := write(req, timeout)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-pairChan.ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, ErrTimeout
	}
}

func (s *sessionTCP) Exchange(req Message, timeout time.Duration) (Message, error) {
	if req.Token() == nil {
		return nil, ErrTokenNotSet
	}
	return s.exchangeFunc(req, timeout, &sessionResp{make(chan Message, 1)}, s.WriteMsg)
}

func (s *sessionUDP) Exchange(req Message, timeout time.Duration) (Message, error) {
	pairChan := &sessionResp{make(chan Message, 1)}
	s.mapMsgIdPairsLock.Lock()
	for s.mapMsgIdPairs[req.MessageID()] != nil {
		return nil, ErrTokenAlreadyExist
	}
	s.mapMsgIdPairs[req.MessageID()] = pairChan
	s.mapMsgIdPairsLock.Unlock()

	defer func() {
		s.mapMsgIdPairsLock.Lock()
		s.mapMsgIdPairs[req.MessageID()] = nil
		s.mapMsgIdPairsLock.Unlock()
	}()
	return s.exchangeFunc(req, timeout, pairChan, s.WriteMsg)
}

// WriteMsg implements the Session.WriteMsg method.
func (s *sessionTCP) WriteMsg(m Message, timeout time.Duration) error {
	req, err := m.MarshalBinary()
	if err != nil {
		return err
	}
	peerMaxMessageSize := int(atomic.LoadUint32(&s.peerMaxMessageSize))
	if peerMaxMessageSize != 0 && len(req) > peerMaxMessageSize {
		//TODO blockwise transfer + BERT to device
		return ErrMsgTooLarge
	}
	return s.connection.write(&writeReqTCP{writeReqBase{req: req, respChan: make(chan error, 1)}}, timeout)
}

// WriteMsg implements the Session.WriteMsg method.
func (s *sessionUDP) WriteMsg(m Message, timeout time.Duration) error {
	req, err := m.MarshalBinary()
	if err != nil {
		return err
	}

	//TODO blockwise transfer to device
	if len(req) > int(s.srv.MaxMessageSize) {
		return ErrMsgTooLarge
	}
	return s.connection.write(&writeReqUDP{writeReqBase{req: req, respChan: make(chan error, 1)}, s.sessionUDPData}, timeout)
}

func (s *sessionBase) handleBasePairMsg(m Message) bool {
	var token [MaxTokenSize]byte
	copy(token[:], m.Token())
	s.mapPairsLock.Lock()
	pair := s.mapPairs[token]
	s.mapPairsLock.Unlock()
	if pair != nil {
		select {
		case pair.ch <- m:
		default:
			log.Fatal("Exactly one message can be send to pair. This is second message.")
		}

		return true
	}
	return false
}

func (s *sessionTCP) handlePairMsg(m Message) bool {
	return s.handleBasePairMsg(m)
}

func (s *sessionUDP) handlePairMsg(m Message) bool {
	if s.handleBasePairMsg(m) {
		return true
	}
	s.mapMsgIdPairsLock.Lock()
	pair := s.mapMsgIdPairs[m.MessageID()]
	s.mapMsgIdPairsLock.Unlock()
	if pair != nil {
		select {
		case pair.ch <- m:
		default:
			log.Fatal("Exactly one message can be send to pair. This is second message.")
		}
		return true
	}
	return false
}

func (s *sessionTCP) sendCSM() error {
	token, err := GenerateToken(MaxTokenSize)
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  CSM,
		Token: []byte(token),
	})
	req.AddOption(MaxMessageSize, uint32(s.srv.MaxMessageSize))
	//TODO blockwise
	//req.AddOption(coap.BlockWiseTransfer)
	return s.WriteMsg(req, 1*time.Second)
}

func (s *sessionTCP) setPeerMaxMeesageSize(val uint32) {
	atomic.StoreUint32(&s.peerMaxMessageSize, val)
}

func (s *sessionTCP) setPeerBlockWiseTransfer(val bool) {
	v := uint32(0)
	if val {
		v = 1
	}
	atomic.StoreUint32(&s.peerBlockWiseTransfer, v)
}

func (s *sessionUDP) sendPong(m Message) error {
	fmt.Printf("sendPong\n")
	req := s.NewMessage(MessageParams{
		Type:      Reset,
		Code:      GET,
		MessageID: m.MessageID(),
	})
	return s.WriteMsg(req, 1*time.Second)
}

func (s *sessionTCP) sendPong(m Message) error {
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Pong,
		Token: m.Token(),
	})
	return s.WriteMsg(req, 1*time.Second)
}

func (s *sessionTCP) handleSignals(m Message) bool {
	switch m.Code() {
	case CSM:
		if size, ok := m.Option(MaxMessageSize).(uint32); ok {
			s.setPeerMaxMeesageSize(size)
		}
		if m.Option(BlockWiseTransfer) != nil {
			s.setPeerBlockWiseTransfer(true)
		}
		return true
	case Ping:
		if m.Option(Custody) != nil {
			//TODO
		}
		s.sendPong(m)
		return true
	case Release:
		if _, ok := m.Option(AlternativeAddress).(string); ok {
			//TODO
		}
		return true
	case Abort:
		if _, ok := m.Option(BadCSMOption).(uint32); ok {
			//TODO
		}
		return true
	}
	return false
}

func (s *sessionUDP) handleSignals(req Message) bool {
	switch req.Code() {
	// handle of udp ping
	case Empty:
		if req.Type() == Confirmable && req.AllOptions().Len() == 0 && (req.Payload() == nil || len(req.Payload()) == 0) {
			s.sendPong(req)
			return true
		}
	}
	return false
}
