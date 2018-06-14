package coap

import (
	"log"
	"net"
	"sync"
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
	// Exchange writes message and wait response for - paired by token and msgid
	// it is safe to use in goroutines
	Exchange(req Message, timeout time.Duration) (Message, error)

	// Message was handled by pair
	HandlePairMsg(req Message) bool

	// close session with error
	closeWithError(err error) error
}

// NewSessionUDP create new session for UDP connection
func NewSessionUDP(connection conn, srv *Server, sessionUDPData *SessionUDPData) Session {
	s := &sessionUDP{sessionBase: sessionBase{srv: srv, mapPairs: make(map[[8]byte](*sessionResp)), connection: connection}, sessionUDPData: sessionUDPData}
	return s
}

// NewSessionTCP create new session for TCP connection
func NewSessionTCP(connection conn, srv *Server) Session {
	s := &sessionTCP{sessionBase: sessionBase{srv: srv, mapPairs: make(map[[8]byte](*sessionResp)), connection: connection}}
	return s
}

type sessionResp struct {
	ch chan Message // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv        *Server
	connection conn

	pairNextToken uint32                   //to create unique token for connection WriteMsgAndWait
	mapPairs      map[[8]byte]*sessionResp //storage of channel Message
	mapPairsLock  sync.Mutex               //to sync add remove token

}

type sessionUDP struct {
	sessionBase
	sessionUDPData *SessionUDPData // oob data to get egress interface right
}

type sessionTCP struct {
	sessionBase
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

func (s *sessionBase) exchangeFunc(req Message, timeout time.Duration, write func(msg Message, timeout time.Duration) error) (Message, error) {
	if req.Token() == nil {
		return nil, ErrTokenNotSet
	}
	var pairToken [8]byte
	copy(pairToken[:], req.Token())
	s.mapPairsLock.Lock()
	for s.mapPairs[pairToken] != nil {
		return nil, ErrTokenAlreadyExist
	}
	pair := &sessionResp{make(chan Message, 1)}
	s.mapPairs[pairToken] = pair
	s.mapPairsLock.Unlock()

	defer func() {
		s.mapPairsLock.Lock()
		s.mapPairs[pairToken] = nil
		s.mapPairsLock.Unlock()
	}()

	err := write(req, timeout)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-pair.ch:
		return resp, nil
	case <-time.After(timeout):
		return nil, ErrTimeout
	}
}

func (s *sessionTCP) Exchange(req Message, timeout time.Duration) (Message, error) {
	return s.exchangeFunc(req, timeout, s.WriteMsg)
}

func (s *sessionUDP) Exchange(req Message, timeout time.Duration) (Message, error) {
	return s.exchangeFunc(req, timeout, s.WriteMsg)
}

// WriteMsg implements the Session.WriteMsg method.
func (s *sessionTCP) WriteMsg(m Message, timeout time.Duration) error {
	return s.connection.Write(&writeReqTCP{writeReqBase{req: m, respChan: make(chan error, 1)}}, timeout)
}

// WriteMsg implements the Session.WriteMsg method.
func (s *sessionUDP) WriteMsg(m Message, timeout time.Duration) error {
	return s.connection.Write(&writeReqUDP{writeReqBase{req: m, respChan: make(chan error, 1)}, s.sessionUDPData}, timeout)
}

func (s *sessionBase) HandlePairMsg(m Message) bool {
	var token [8]byte
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
