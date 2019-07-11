package coap

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type sessionResp struct {
	ch chan *Request // channel must have size 1 for non-blocking write to channel
}

type sessionBase struct {
	srv      *Server
	handler  *TokenHandler
	sequence uint64

	blockWiseTransfer    bool
	blockWiseTransferSzx uint32                                         //BlockWiseSzx
	mapPairs             map[[MaxTokenSize]byte]map[uint16]*sessionResp //storage of channel Message
	mapPairsLock         sync.Mutex                                     //to sync add remove token
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

func (s *sessionBase) TokenHandler() *TokenHandler {
	return s.handler
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

func (s *sessionBase) exchangeWithContext(ctx context.Context, req Message, writeMsgWithContext func(context.Context, Message) error) (Message, error) {
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

	err := writeMsgWithContext(ctx, req)
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

func (s *sessionBase) handlePairMsg(w ResponseWriter, r *Request) bool {
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
