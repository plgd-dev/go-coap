package coap

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	coapNet "github.com/go-ocf/go-coap/net"
)

// A networkSession interface is used by an COAP handler to
// server data in session.
type networkSession interface {
	// LocalAddr returns the net.Addr of the server
	LocalAddr() net.Addr
	// RemoteAddr returns the net.Addr of the client that sent the current request.
	RemoteAddr() net.Addr
	// WriteContextMsg writes a reply back to the client.
	WriteMsgWithContext(ctx context.Context, resp Message) error
	// Close closes the connection.
	Close() error
	// Return type of network
	IsTCP() bool
	// Create message for response via writter
	NewMessage(params MessageParams) Message
	// ExchangeContext writes message and wait for response - paired by token and msgid
	// it is safe to use in goroutines
	ExchangeWithContext(ctx context.Context, req Message) (Message, error)
	// Send ping to peer and wait for pong
	PingWithContext(ctx context.Context) error
	// Sequence discontinuously unique growing number for connection.
	Sequence() uint64

	// handlePairMsg Message was handled by pair
	handlePairMsg(w ResponseWriter, r *Request) bool

	// handleSignals Message below to signals
	handleSignals(w ResponseWriter, r *Request) bool

	// sendPong create pong by m and send it
	sendPong(w ResponseWriter, r *Request) error

	// close session with error
	closeWithError(err error) error

	TokenHandler() *TokenHandler

	// BlockWiseTransferEnabled
	blockWiseEnabled() bool
	// BlockWiseTransferSzx
	blockWiseSzx() BlockWiseSzx
	// MaxPayloadSize
	blockWiseMaxPayloadSize(peer BlockWiseSzx) (int, BlockWiseSzx)

	blockWiseIsValid(szx BlockWiseSzx) bool
}

type connUDP interface {
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Close() error
	ReadWithContext(ctx context.Context, buffer []byte) (int, *coapNet.ConnUDPContext, error)
	WriteWithContext(ctx context.Context, udpCtx *coapNet.ConnUDPContext, buffer []byte) error
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
		sessionBase: sessionBase{
			srv:                  srv,
			handler:              &TokenHandler{tokenHandlers: make(map[[MaxTokenSize]byte]HandlerFunc)},
			blockWiseTransfer:    BlockWiseTransfer,
			blockWiseTransferSzx: uint32(BlockWiseTransferSzx),
		},
		connection:     connection,
		sessionUDPData: sessionUDPData,
		mapPairs:       make(map[[MaxTokenSize]byte]map[uint16](*sessionResp)),
	}
	return s, nil
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
		mapPairs:           make(map[[MaxTokenSize]byte](*sessionResp)),
		peerMaxMessageSize: uint32(srv.MaxMessageSize),
		connection:         connection,
		sessionBase: sessionBase{
			srv:                  srv,
			handler:              &TokenHandler{tokenHandlers: make(map[[MaxTokenSize]byte]HandlerFunc)},
			blockWiseTransfer:    BlockWiseTransfer,
			blockWiseTransferSzx: uint32(BlockWiseTransferSzx),
		},
	}

	if !s.srv.DisableTCPSignalMessages {
		if err := s.sendCSM(); err != nil {
			return nil, err
		}
	}

	return s, nil
}

type sessionResp struct {
	ch chan *Request // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv      *Server
	handler  *TokenHandler
	sequence uint64

	blockWiseTransfer    bool
	blockWiseTransferSzx uint32 //BlockWiseSzx
}

type sessionUDP struct {
	sessionBase
	connection     connUDP
	sessionUDPData *coapNet.ConnUDPContext                        // oob data to get egress interface right
	mapPairs       map[[MaxTokenSize]byte]map[uint16]*sessionResp //storage of channel Message
	mapPairsLock   sync.Mutex                                     //to sync add remove token
}

type sessionTCP struct {
	sessionBase
	connection   *coapNet.Conn
	mapPairs     map[[MaxTokenSize]byte]*sessionResp //storage of channel Message
	mapPairsLock sync.Mutex                          //to sync add remove token

	peerBlockWiseTransfer uint32
	peerMaxMessageSize    uint32
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionUDP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionTCP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (s *sessionUDP) RemoteAddr() net.Addr {
	return s.sessionUDPData.RemoteAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
func (s *sessionTCP) RemoteAddr() net.Addr {
	return s.connection.RemoteAddr()
}

// BlockWiseTransferEnabled
func (s *sessionUDP) blockWiseEnabled() bool {
	return s.blockWiseTransfer
}

func (s *sessionTCP) blockWiseEnabled() bool {
	return s.blockWiseTransfer /*&& atomic.LoadUint32(&s.peerBlockWiseTransfer) != 0*/
}

func (s *sessionBase) blockWiseSzx() BlockWiseSzx {
	return BlockWiseSzx(atomic.LoadUint32(&s.blockWiseTransferSzx))
}

func (s *sessionBase) setBlockWiseSzx(szx BlockWiseSzx) {
	atomic.StoreUint32(&s.blockWiseTransferSzx, uint32(szx))
}

func (s *sessionBase) Sequence() uint64 {
	return atomic.AddUint64(&s.sequence, 1)
}

func (s *sessionBase) blockWiseMaxPayloadSize(peer BlockWiseSzx) (int, BlockWiseSzx) {
	szx := s.blockWiseSzx()
	if peer < szx {
		return szxToBytes[peer], peer
	}
	return szxToBytes[szx], szx
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

func (s *sessionUDP) blockWiseIsValid(szx BlockWiseSzx) bool {
	return szx <= BlockWiseSzx1024
}

func (s *sessionTCP) blockWiseIsValid(szx BlockWiseSzx) bool {
	return true
}

func (s *sessionBase) TokenHandler() *TokenHandler {
	return s.handler
}

func (s *sessionUDP) closeWithError(err error) error {
	s.srv.sessionUDPMapLock.Lock()
	delete(s.srv.sessionUDPMap, s.sessionUDPData.Key())
	s.srv.sessionUDPMapLock.Unlock()
	c := ClientConn{commander: &ClientCommander{s}}
	s.srv.NotifySessionEndFunc(&c, err)

	return err
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
		Code:      Empty,
		MessageID: GenerateMessageID(),
	})
	resp, err := s.ExchangeWithContext(ctx, req)
	if err != nil {
		return err
	}
	if resp.Type() == Reset {
		return nil
	}
	return ErrInvalidResponse
}

func (s *sessionTCP) PingWithContext(ctx context.Context) error {
	if s.srv.DisableTCPSignalMessages {
		return fmt.Errorf("cannot send ping: TCP Signal messages are disabled")
	}
	token, err := GenerateToken()
	if err != nil {
		return err
	}
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Ping,
		Token: []byte(token),
	})
	resp, err := s.ExchangeWithContext(ctx, req)
	if err != nil {
		return err
	}
	if resp.Code() == Pong {
		return nil
	}
	return ErrInvalidResponse
}

// Close implements the networkSession.Close method
func (s *sessionUDP) Close() error {
	return s.closeWithError(nil)
}

func (s *sessionTCP) closeWithError(err error) error {
	if s.connection != nil {
		c := ClientConn{commander: &ClientCommander{s}}
		s.srv.NotifySessionEndFunc(&c, err)
		e := s.connection.Close()
		//s.connection = nil
		if e == nil {
			e = err
		}
		return e
	}
	return err
}

// Close implements the networkSession.Close method
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

// Close implements the networkSession.Close method
func (s *sessionUDP) IsTCP() bool {
	return false
}

// Close implements the networkSession.Close method
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

func (s *sessionTCP) ExchangeWithContext(ctx context.Context, req Message) (Message, error) {
	if err := validateMsg(req); err != nil {
		return nil, fmt.Errorf("cannot exchange: %v", err)
	}
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

	err := s.WriteMsgWithContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("cannot exchange: %v", err)
	}
	select {
	case request := <-pairChan.ch:
		return request.Msg, nil
	case <-ctx.Done():
		if ctx.Err() != nil {
			return nil, fmt.Errorf("cannot exchange: %v", ctx.Err())
		}
		return nil, fmt.Errorf("cannot exchange: cancelled")
	}
}

func (s *sessionUDP) ExchangeWithContext(ctx context.Context, req Message) (Message, error) {
	if err := validateMsg(req); err != nil {
		return nil, fmt.Errorf("cannot exchange: %v", err)
	}
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

	err := s.WriteMsgWithContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("cannot exchange: %v", err)
	}
	select {
	case request := <-pairChan.ch:
		return request.Msg, nil
	case <-ctx.Done():
		if ctx.Err() != nil {
			return nil, fmt.Errorf("cannot exchange: %v", ctx.Err())
		}
		return nil, fmt.Errorf("cannot exchange: cancelled")
	}
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

func (s *sessionUDP) WriteMsgWithContext(ctx context.Context, req Message) error {
	buffer := bytes.NewBuffer(make([]byte, 0, 1500))
	err := req.MarshalBinary(buffer)
	if err != nil {
		return fmt.Errorf("cannot write msg to udp connection %v", err)
	}
	return s.connection.WriteWithContext(ctx, s.sessionUDPData, buffer.Bytes())
}

func validateMsg(msg Message) error {
	if msg.Payload() != nil && msg.Option(ContentFormat) == nil {
		return ErrContentFormatNotSet
	}
	if msg.Payload() == nil && msg.Option(ContentFormat) != nil {
		return ErrInvalidPayload
	}
	//TODO check size of m
	return nil
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

func (s *sessionUDP) sendPong(w ResponseWriter, r *Request) error {
	resp := r.Client.NewMessage(MessageParams{
		Type:      Reset,
		Code:      Empty,
		MessageID: r.Msg.MessageID(),
	})
	return w.WriteMsgWithContext(r.Ctx, resp)
}

func (s *sessionTCP) sendPong(w ResponseWriter, r *Request) error {
	req := s.NewMessage(MessageParams{
		Type:  NonConfirmable,
		Code:  Pong,
		Token: r.Msg.Token(),
	})
	return w.WriteMsgWithContext(r.Ctx, req)
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
	if !r.Client.networkSession().handleSignals(w, r) {
		next(w, r)
	}
}

func handlePairMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.Client.networkSession().handlePairMsg(w, r) {
		next(w, r)
	}
}
