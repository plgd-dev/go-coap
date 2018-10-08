package coap

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// A SessionNet interface is used by an COAP handler to
// server data in session.
type SessionNet interface {
	// LocalAddr returns the net.Addr of the server
	LocalAddr() net.Addr
	// RemoteAddr returns the net.Addr of the client that sent the current request.
	RemoteAddr() net.Addr
	// WriteMsg writes a reply back to the client.
	Write(resp Message) error
	// Close closes the connection.
	Close() error
	// Return type of network
	IsTCP() bool
	// Create message for response via writter
	NewMessage(params MessageParams) Message
	// Exchange writes message and wait for response - paired by token and msgid
	// it is safe to use in goroutines
	Exchange(req Message) (Message, error)
	// Send ping to peer and wait for pong
	Ping(timeout time.Duration) error
	// SetReadDeadline set read deadline for timeout for Exchange
	SetReadDeadline(timeout time.Duration)
	// SetWriteDeadline set write deadline for timeout for Exchange and Write
	SetWriteDeadline(timeout time.Duration)
	// ReadDeadline get read deadline
	ReadDeadline() time.Duration
	// WriteDeadline get read writeline
	WriteDeadline() time.Duration

	// handlePairMsg Message was handled by pair
	handlePairMsg(w ResponseWriter, r *Request) bool

	// handleSignals Message below to signals
	handleSignals(w ResponseWriter, r *Request) bool

	// sendPong create pong by m and send it
	sendPong(w ResponseWriter, r *Request) error

	// close session with error
	closeWithError(err error) error

	exchangeTimeout(req Message, writeDeadline, readDeadline time.Duration) (Message, error)

	sessionHandler() *sessionHandler

	// BlockWiseTransferEnabled
	blockWiseEnabled() bool
	// BlockWiseTransferSzx
	blockWiseSzx() BlockSzx
	// MaxPayloadSize
	blockWiseMaxPayloadSize(peer BlockSzx) int

	blockWiseIsValid(szx BlockSzx) bool
}

// NewSessionUDP create new session for UDP connection
func newSessionUDP(connection Conn, srv *Server, sessionUDPData *SessionUDPData) (SessionNet, error) {

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockSzx1024
	if srv.BlockWiseTransfer != nil {
		BlockWiseTransfer = *srv.BlockWiseTransfer
	}
	if srv.BlockWiseTransferSzx != nil {
		BlockWiseTransferSzx = *srv.BlockWiseTransferSzx
	}

	if BlockWiseTransfer && BlockWiseTransferSzx == BlockSzxBERT {
		return nil, ErrInvalidBlockSzx
	}

	s := &sessionUDP{
		sessionBase: sessionBase{
			srv:                  srv,
			connection:           connection,
			readDeadline:         30 * time.Second,
			writeDeadline:        30 * time.Second,
			handler:              &sessionHandler{tokenHandlers: make(map[[MaxTokenSize]byte]func(w ResponseWriter, r *Request, next HandlerFunc))},
			blockWiseTransfer:    BlockWiseTransfer,
			blockWiseTransferSzx: BlockWiseTransferSzx,
		},
		sessionUDPData: sessionUDPData,
		mapPairs:       make(map[[MaxTokenSize]byte]map[uint16](*sessionResp)),
	}
	return s, nil
}

// newSessionTCP create new session for TCP connection
func newSessionTCP(connection Conn, srv *Server) (SessionNet, error) {
	BlockWiseTransfer := false
	BlockWiseTransferSzx := BlockSzxBERT
	if srv.BlockWiseTransfer != nil {
		BlockWiseTransfer = *srv.BlockWiseTransfer
	}
	if srv.BlockWiseTransferSzx != nil {
		BlockWiseTransferSzx = *srv.BlockWiseTransferSzx
	}
	s := &sessionTCP{
		mapPairs:           make(map[[MaxTokenSize]byte](*sessionResp)),
		peerMaxMessageSize: uint32(srv.MaxMessageSize),
		sessionBase: sessionBase{
			srv:                  srv,
			connection:           connection,
			readDeadline:         30 * time.Second,
			writeDeadline:        30 * time.Second,
			handler:              &sessionHandler{tokenHandlers: make(map[[MaxTokenSize]byte]func(w ResponseWriter, r *Request, next HandlerFunc))},
			blockWiseTransfer:    BlockWiseTransfer,
			blockWiseTransferSzx: BlockWiseTransferSzx,
		},
	}
	/*
		if err := s.sendCSM(); err != nil {
			return nil, err
		}
	*/

	return s, nil
}

type sessionResp struct {
	ch chan *Request // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv           *Server
	connection    Conn
	readDeadline  time.Duration
	writeDeadline time.Duration
	handler       *sessionHandler

	blockWiseTransfer    bool
	blockWiseTransferSzx BlockSzx
}

type sessionUDP struct {
	sessionBase
	sessionUDPData *SessionUDPData                                // oob data to get egress interface right
	mapPairs       map[[MaxTokenSize]byte]map[uint16]*sessionResp //storage of channel Message
	mapPairsLock   sync.Mutex                                     //to sync add remove token
}

type sessionTCP struct {
	sessionBase

	mapPairs     map[[MaxTokenSize]byte]*sessionResp //storage of channel Message
	mapPairsLock sync.Mutex                          //to sync add remove token

	peerBlockWiseTransfer uint32
	peerMaxMessageSize    uint32
}

// LocalAddr implements the SessionNet.LocalAddr method.
func (s *sessionUDP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// LocalAddr implements the SessionNet.LocalAddr method.
func (s *sessionTCP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the SessionNet.RemoteAddr method.
func (s *sessionUDP) RemoteAddr() net.Addr {
	return s.sessionUDPData.RemoteAddr()
}

// RemoteAddr implements the SessionNet.RemoteAddr method.
func (s *sessionTCP) RemoteAddr() net.Addr {
	return s.connection.RemoteAddr()
}

func (s *sessionBase) SetReadDeadline(timeout time.Duration) {
	s.readDeadline = timeout
}

func (s *sessionBase) SetWriteDeadline(timeout time.Duration) {
	s.writeDeadline = timeout
}

func (s *sessionBase) ReadDeadline() time.Duration {
	return s.readDeadline
}

// WriteDeadline get read writeline
func (s *sessionBase) WriteDeadline() time.Duration {
	return s.writeDeadline
}

// BlockWiseTransferEnabled
func (s *sessionUDP) blockWiseEnabled() bool {
	return s.blockWiseTransfer
}

func (s *sessionTCP) blockWiseEnabled() bool {
	return s.blockWiseTransfer /*&& atomic.LoadUint32(&s.peerBlockWiseTransfer) != 0*/
}

func (s *sessionBase) blockWiseSzx() BlockSzx {
	return s.blockWiseTransferSzx
}

func (s *sessionBase) blockWiseMaxPayloadSize(peer BlockSzx) int {
	if peer < s.blockWiseTransferSzx {
		return SZXVal[peer]
	}
	return SZXVal[s.blockWiseTransferSzx]
}

func (s *sessionTCP) blockWiseMaxPayloadSize(peer BlockSzx) int {
	if s.blockWiseTransferSzx == BlockSzxBERT && peer == BlockSzxBERT {
		m := atomic.LoadUint32(&s.peerMaxMessageSize)
		if m == 0 {
			m = maxMessageSize
		}
		return int(m - (m % 1024))
	}
	return s.sessionBase.blockWiseMaxPayloadSize(peer)
}

func (s *sessionUDP) blockWiseIsValid(szx BlockSzx) bool {
	return szx <= BlockSzx1024
}

func (s *sessionTCP) blockWiseIsValid(szx BlockSzx) bool {
	return true
}

func (s *sessionBase) sessionHandler() *sessionHandler {
	return s.handler
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
		MessageID: GenerateMessageID(),
	})
	resp, err := s.exchangeTimeout(req, timeout, timeout)
	if err != nil {
		return err
	}
	if resp.Type() == Reset {
		return nil
	}
	return ErrInvalidResponse
}

func (s *sessionTCP) Ping(timeout time.Duration) error {
	token, err := GenerateToken()
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Ping,
		Token: []byte(token),
	})
	resp, err := s.exchangeTimeout(req, timeout, timeout)
	if err != nil {
		return err
	}
	if resp.Code() == Pong {
		return nil
	}
	return ErrInvalidResponse
}

// Close implements the SessionNet.Close method
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

// Close implements the SessionNet.Close method
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

// Close implements the SessionNet.Close method
func (s *sessionUDP) IsTCP() bool {
	return false
}

// Close implements the SessionNet.Close method
func (s *sessionTCP) IsTCP() bool {
	return true
}

func (s *sessionBase) exchangeFunc(req Message, writeTimeout, readTimeout time.Duration, pairChan *sessionResp, write func(msg Message, timeout time.Duration) error) (Message, error) {

	err := write(req, writeTimeout)
	if err != nil {
		return nil, err
	}

	select {
	case resp := <-pairChan.ch:
		return resp.Msg, nil
	case <-time.After(readTimeout):
		return nil, ErrTimeout
	}
}

// Write implements the SessionNet.Write method.
func (s *sessionTCP) Exchange(m Message) (Message, error) {
	return s.exchangeTimeout(m, s.writeDeadline, s.readDeadline)
}

// Write implements the SessionNet.Write method.
func (s *sessionUDP) Exchange(m Message) (Message, error) {
	return s.exchangeTimeout(m, s.writeDeadline, s.readDeadline)
}

func (s *sessionTCP) exchangeTimeout(req Message, writeDeadline, readDeadline time.Duration) (Message, error) {
	if req.Token() == nil {
		return nil, ErrTokenNotExist
	}

	pairChan := &sessionResp{make(chan *Request, 1)}

	var pairToken [MaxTokenSize]byte
	copy(pairToken[:], req.Token())
	s.mapPairsLock.Lock()
	if s.mapPairs[pairToken] != nil {
		return nil, ErrTokenAlreadyExist
	}

	s.mapPairs[pairToken] = pairChan
	s.mapPairsLock.Unlock()

	defer func() {
		if req.Token() != nil {
			s.mapPairsLock.Lock()
			delete(s.mapPairs, pairToken)
			s.mapPairsLock.Unlock()
		}
	}()

	return s.exchangeFunc(req, writeDeadline, readDeadline, pairChan, s.writeTimeout)
}

func (s *sessionUDP) exchangeTimeout(req Message, writeDeadline, readDeadline time.Duration) (Message, error) {
	//register msgid to token
	pairChan := &sessionResp{make(chan *Request, 1)}
	var pairToken [MaxTokenSize]byte
	copy(pairToken[:], req.Token())
	s.mapPairsLock.Lock()
	if s.mapPairs[pairToken] == nil {
		s.mapPairs[pairToken] = make(map[uint16]*sessionResp)
	}
	if s.mapPairs[pairToken][req.MessageID()] != nil {
		s.mapPairsLock.Unlock()
		return nil, ErrTokenAlreadyExist
	}
	s.mapPairs[pairToken][req.MessageID()] = pairChan
	s.mapPairsLock.Unlock()

	defer func() {
		s.mapPairsLock.Lock()
		delete(s.mapPairs[pairToken], req.MessageID())
		if len(s.mapPairs[pairToken]) == 0 {
			delete(s.mapPairs, pairToken)
		}
		s.mapPairsLock.Unlock()
	}()

	return s.exchangeFunc(req, writeDeadline, readDeadline, pairChan, s.writeTimeout)
}

// Write implements the SessionNet.Write method.
func (s *sessionTCP) Write(m Message) error {
	return s.writeTimeout(m, s.writeDeadline)
}

func (s *sessionUDP) Write(m Message) error {
	return s.writeTimeout(m, s.writeDeadline)
}

func (s *sessionTCP) writeTimeout(m Message, timeout time.Duration) error {
	req, err := m.MarshalBinary()
	if err != nil {
		return err
	}
	peerMaxMessageSize := int(atomic.LoadUint32(&s.peerMaxMessageSize))
	if peerMaxMessageSize != 0 && len(req) > peerMaxMessageSize {
		//TODO blockWise transfer + BERT to device
		return ErrMsgTooLarge
	}
	return s.connection.write(&writeReqTCP{writeReqBase{req: req, respChan: make(chan error, 1)}}, timeout)
}

// WriteMsg implements the SessionNet.WriteMsg method.
func (s *sessionUDP) writeTimeout(m Message, timeout time.Duration) error {
	req, err := m.MarshalBinary()
	if err != nil {
		return err
	}

	//TODO blockWise transfer to device
	if len(req) > int(s.srv.MaxMessageSize) {
		return ErrMsgTooLarge
	}
	return s.connection.write(&writeReqUDP{writeReqBase{req: req, respChan: make(chan error, 1)}, s.sessionUDPData}, timeout)
}

func (s *sessionTCP) handlePairMsg(w ResponseWriter, r *Request) bool {
	var token [MaxTokenSize]byte
	copy(token[:], r.Msg.Token())
	s.mapPairsLock.Lock()
	pair := s.mapPairs[token]
	s.mapPairsLock.Unlock()
	if pair != nil {
		select {
		case pair.ch <- r:
		default:
			log.Fatal("Exactly one message can be send to pair. This is second message.")
		}

		return true
	}
	return false
}

func (s *sessionUDP) handlePairMsg(w ResponseWriter, r *Request) bool {
	var token [MaxTokenSize]byte
	copy(token[:], r.Msg.Token())
	//validate token

	s.mapPairsLock.Lock()
	pair := s.mapPairs[token][r.Msg.MessageID()]
	s.mapPairsLock.Unlock()
	if pair != nil {
		select {
		case pair.ch <- r:
		default:
			log.Fatal("Exactly one message can be send to pair. This is second message.")
		}
		return true
	}
	return false
}

func (s *sessionTCP) sendCSM() error {
	token, err := GenerateToken()
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  CSM,
		Token: []byte(token),
	})
	req.AddOption(MaxMessageSize, uint32(s.srv.MaxMessageSize))
	if s.blockWiseEnabled() {
		req.AddOption(BlockWiseTransfer, nil)
	}
	return s.Write(req)
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

func (s *sessionUDP) sendPong(w ResponseWriter, r *Request) error {
	resp := r.SessionNet.NewMessage(MessageParams{
		Type:      Reset,
		Code:      Empty,
		MessageID: r.Msg.MessageID(),
	})
	return w.Write(resp)
}

func (s *sessionTCP) sendPong(w ResponseWriter, r *Request) error {
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Pong,
		Token: r.Msg.Token(),
	})
	return w.Write(req)
}

func (s *sessionTCP) handleSignals(w ResponseWriter, r *Request) bool {
	switch r.Msg.Code() {
	case CSM:
		maxmsgsize := uint32(maxMessageSize)
		if size, ok := r.Msg.Option(MaxMessageSize).(uint32); ok {
			s.setPeerMaxMessageSize(size)
			maxmsgsize = size
		}
		if r.Msg.Option(BlockWiseTransfer) != nil {
			s.setPeerBlockWiseTransfer(true)
			switch s.blockWiseSzx() {
			case BlockSzxBERT:
				if SZXVal[BlockSzx1024] < int(maxmsgsize) {
					s.sessionBase.blockWiseTransferSzx = BlockSzxBERT
				}
				for i := BlockSzx512; i > BlockSzx16; i-- {
					if SZXVal[i] < int(maxmsgsize) {
						s.sessionBase.blockWiseTransferSzx = i
					}
				}
				s.sessionBase.blockWiseTransferSzx = BlockSzx16
			default:
				for i := s.blockWiseSzx(); i > BlockSzx16; i-- {
					if SZXVal[i] < int(maxmsgsize) {
						s.sessionBase.blockWiseTransferSzx = i
					}
				}
				s.sessionBase.blockWiseTransferSzx = BlockSzx16
			}
		}
		return true
	case Ping:
		if r.Msg.Option(Custody) != nil {
			//TODO
		}
		s.sendPong(w, r)
		return true
	case Release:
		if _, ok := r.Msg.Option(AlternativeAddress).(string); ok {
			//TODO
		}
		return true
	case Abort:
		if _, ok := r.Msg.Option(BadCSMOption).(uint32); ok {
			//TODO
		}
		return true
	}
	return false
}

func (s *sessionUDP) handleSignals(w ResponseWriter, r *Request) bool {
	switch r.Msg.Code() {
	// handle of udp ping
	case Empty:
		if r.Msg.Type() == Confirmable && r.Msg.AllOptions().Len() == 0 && (r.Msg.Payload() == nil || len(r.Msg.Payload()) == 0) {
			s.sendPong(w, r)
			return true
		}
	}
	return false
}

func handleSignalMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.SessionNet.handleSignals(w, r) {
		next(w, r)
	}
}

func handlePairMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.SessionNet.handlePairMsg(w, r) {
		next(w, r)
	}
}

func handleBySessionHandler(w ResponseWriter, r *Request, next HandlerFunc) {
	r.SessionNet.sessionHandler().handle(w, r, next)
}

type sessionHandler struct {
	tokenHandlers     map[[MaxTokenSize]byte]func(w ResponseWriter, r *Request, next HandlerFunc)
	tokenHandlersLock sync.Mutex
}

func (s *sessionHandler) handle(w ResponseWriter, r *Request, next HandlerFunc) {
	//validate token
	var token [MaxTokenSize]byte
	copy(token[:], r.Msg.Token())
	s.tokenHandlersLock.Lock()
	h := s.tokenHandlers[token]
	s.tokenHandlersLock.Unlock()
	if h != nil {
		h(w, r, next)
		return
	}
	if next != nil {
		next(w, r)
	}
}

func (s *sessionHandler) add(t []byte, h func(w ResponseWriter, r *Request, next HandlerFunc)) error {
	var token [MaxTokenSize]byte
	copy(token[:], t)
	s.tokenHandlersLock.Lock()
	defer s.tokenHandlersLock.Unlock()
	if s.tokenHandlers[token] != nil {
		return ErrTokenAlreadyExist
	}
	s.tokenHandlers[token] = h
	return nil
}

func (s *sessionHandler) remove(t []byte) error {
	var token [MaxTokenSize]byte
	copy(token[:], t)
	s.tokenHandlersLock.Lock()
	defer s.tokenHandlersLock.Unlock()
	if s.tokenHandlers[token] == nil {
		return ErrTokenNotExist
	}
	delete(s.tokenHandlers, token)
	return nil
}
