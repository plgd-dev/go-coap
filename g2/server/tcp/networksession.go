package coapservertcp

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	coap "github.com/go-ocf/go-coap/g2/message"
	coapMsg "github.com/go-ocf/go-coap/g2/message/tcp"
)

// newSessionTCP create new session for TCP connection
func newSessionTCP(connection *connTCP, srv *Server) (*sessionTCP, error) {
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
		sessionBase: sessionBase{
			srv:                  srv,
			connection:           connection,
			readDeadline:         30 * time.Second,
			writeDeadline:        30 * time.Second,
			handler:              &TokenHandler{tokenHandlers: make(map[[MaxTokenSize]byte]HandlerFunc)},
			blockWiseTransfer:    BlockWiseTransfer,
			blockWiseTransferSzx: uint32(BlockWiseTransferSzx),
		},
	}

	if err := s.sendCSM(); err != nil {
		return nil, err
	}

	return s, nil
}

type sessionResp struct {
	ch chan *Request // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv           *Server
	connection    *connTCP
	readDeadline  time.Duration
	writeDeadline time.Duration
	handler       *TokenHandler

	blockWiseTransfer    bool
	blockWiseTransferSzx uint32 //BlockWiseSzx
}

type sessionTCP struct {
	sessionBase

	mapPairs     map[[coap.MaxTokenSize]byte]*sessionResp //storage of channel coapMsg.TCPMessage
	mapPairsLock sync.Mutex                               //to sync add remove token

	peerBlockWiseTransfer uint32
	peerMaxMessageSize    uint32
}

// LocalAddr implements the networkSession.LocalAddr method.
func (s *sessionTCP) LocalAddr() net.Addr {
	return s.connection.LocalAddr()
}

// RemoteAddr implements the networkSession.RemoteAddr method.
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

func (s *sessionTCP) blockWiseEnabled() bool {
	return s.blockWiseTransfer /*&& atomic.LoadUint32(&s.peerBlockWiseTransfer) != 0*/
}

func (s *sessionBase) blockWiseSzx() BlockWiseSzx {
	return BlockWiseSzx(atomic.LoadUint32(&s.blockWiseTransferSzx))
}

func (s *sessionBase) setBlockWiseSzx(szx BlockWiseSzx) {
	atomic.StoreUint32(&s.blockWiseTransferSzx, uint32(szx))
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

func (s *sessionTCP) blockWiseIsValid(szx BlockWiseSzx) bool {
	return true
}

func (s *sessionBase) TokenHandler() *TokenHandler {
	return s.handler
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

func (s *sessionTCP) closeWithError(err error) error {
	if s.connection != nil {
		s.srv.NotifySessionEndFunc(&ClientCommander{s}, err)
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

func (s *sessionBase) exchangeFunc(req coapMsg.TCPMessage, writeTimeout, readTimeout time.Duration, pairChan *sessionResp) (coapMsg.TCPMessage, error) {

	err := s.writeTimeout(req, writeTimeout)
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

// Write implements the networkSession.Write method.
func (s *sessionTCP) Exchange(m coapMsg.TCPMessage) (coapMsg.TCPMessage, error) {
	return s.exchangeTimeout(m, s.writeDeadline, s.readDeadline)
}

func (s *sessionTCP) exchangeTimeout(req coapMsg.TCPMessage, writeDeadline, readDeadline time.Duration) (coapMsg.TCPMessage, error) {
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

	return s.exchangeFunc(req, writeDeadline, readDeadline, pairChan)
}

// Write implements the networkSession.Write method.
func (s *sessionTCP) WriteMsg(m coapMsg.TCPMessage) error {
	return s.writeTimeout(m, s.writeDeadline)
}

func validateMsg(msg coapMsg.TCPMessage) error {
	if msg.Payload() != nil && msg.Option(ContentFormat) == nil {
		return ErrContentFormatNotSet
	}
	if msg.Payload() == nil && msg.Option(ContentFormat) != nil {
		return ErrInvalidPayload
	}
	//TODO check size of m
	return nil
}

func (s *sessionTCP) writeTimeout(m coapMsg.TCPMessage, timeout time.Duration) error {
	if err := validateMsg(m); err != nil {
		return err
	}
	return s.connection.write(&writeReqTCP{writeReqBase{req: m, respChan: make(chan error, 1)}}, timeout)
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
		req.AddOption(BlockWiseTransfer, []byte{})
	}
	return s.WriteMsg(req)
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
		Code:  Pong,
		Token: r.Msg.Token(),
	})
	return w.WriteMsg(req)
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

func handleSignalMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.Client.networkSession.handleSignals(w, r) {
		next(w, r)
	}
}

func handlePairMsg(w ResponseWriter, r *Request, next HandlerFunc) {
	if !r.Client.networkSession.handlePairMsg(w, r) {
		next(w, r)
	}
}
