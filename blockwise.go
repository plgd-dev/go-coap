package coap

import (
	"bytes"
	"fmt"
	"log"
	"time"
)

const (
	maxBlockNumber = uint(1048575)
	blockWiseDebug = false
)

type BlockSzx uint8

const (
	BlockSzx16   BlockSzx = 0
	BlockSzx32   BlockSzx = 1
	BlockSzx64   BlockSzx = 2
	BlockSzx128  BlockSzx = 3
	BlockSzx256  BlockSzx = 4
	BlockSzx512  BlockSzx = 5
	BlockSzx1024 BlockSzx = 6
	BlockSzxBERT BlockSzx = 7
)

var SZXVal = [8]int{
	0: 16,
	1: 32,
	2: 64,
	3: 128,
	4: 256,
	5: 512,
	6: 1024,
	7: 1024,
}

func MarshalBlockOption(szx BlockSzx, blockNumber uint, moreBlocksFollowing bool) (uint32, error) {
	if blockNumber > maxBlockNumber {
		return 0, ErrBlockNumberExceedLimit
	}
	blockVal := uint32(blockNumber << 4)
	m := uint32(0)
	if moreBlocksFollowing {
		m = 1
	}
	blockVal += m << 3
	blockVal += uint32(szx)
	return blockVal, nil
}

func UnmarshalBlockOption(blockVal uint32) (szx BlockSzx, blockNumber uint, moreBlocksFollowing bool, err error) {
	if blockVal > 0xffffff {
		err = ErrBlockInvalidSize
	}

	szx = BlockSzx(blockVal & 0x7) //masking for the SZX
	if (blockVal & 0x8) != 0 {     //masking for the "M"
		moreBlocksFollowing = true
	}
	blockNumber = uint(blockVal) >> 4 //shifting out the SZX and M vals. leaving the block number behind
	if blockNumber > maxBlockNumber {
		err = ErrBlockNumberExceedLimit
	}
	return
}

func exchangeDrivedByPeer(session networkSession, req Message, blockType OptionID) (Message, error) {
	if block, ok := req.Option(blockType).(uint32); ok {
		_, _, more, err := UnmarshalBlockOption(block)
		if err != nil {
			return nil, err
		}
		if more == false {
			// we send all datas to peer -> create empty response
			err := session.Write(req)
			if err != nil {
				return nil, err
			}
			return session.NewMessage(MessageParams{}), nil
		}
	}

	pair := make(chan *Request, 1)
	session.TokenHandler().Add(req.Token(), func(w ResponseWriter, r *Request) {
		select {
		case pair <- r:
		default:
			return
		}
	})
	defer session.TokenHandler().Remove(req.Token())
	err := session.Write(req)
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-pair:
		return resp.Msg, nil
	case <-time.After(session.ReadDeadline()):
		return nil, ErrTimeout
	}
}

type blockWiseSender struct {
	peerDrive    bool
	blockType    OptionID
	expectedCode COAPCode
	origin       Message

	currentNum  uint
	currentSzx  BlockSzx
	currentMore bool
}

func (s *blockWiseSender) coapType() COAPType {
	if s.peerDrive {
		return Acknowledgement
	}
	return Confirmable
}

func (s *blockWiseSender) sizeType() OptionID {
	if s.blockType == Block2 {
		return Size2
	}
	return Size1
}

func newSender(peerDrive bool, blockType OptionID, suggestedSzx BlockSzx, expectedCode COAPCode, origin Message) *blockWiseSender {
	return &blockWiseSender{
		peerDrive:    peerDrive,
		blockType:    blockType,
		currentSzx:   suggestedSzx,
		expectedCode: expectedCode,
		origin:       origin,
	}
}

func (s *blockWiseSender) createReq(b *blockWiseSession) (Message, error) {
	req := b.networkSession.NewMessage(MessageParams{
		Code:      s.origin.Code(),
		Type:      s.coapType(),
		MessageID: s.origin.MessageID(),
		Token:     s.origin.Token(),
	})

	if !s.peerDrive {
		req.SetMessageID(GenerateMessageID())
	}

	for _, option := range s.origin.AllOptions() {
		req.AddOption(option.ID, option.Value)
	}

	req.SetOption(s.sizeType(), len(s.origin.Payload()))

	if s.origin.Payload() != nil && len(s.origin.Payload()) > b.blockWiseMaxPayloadSize(s.currentSzx) {
		req.SetPayload(s.origin.Payload()[:b.blockWiseMaxPayloadSize(s.currentSzx)])
		s.currentMore = true
	} else {
		req.SetPayload(s.origin.Payload())
	}

	block, err := MarshalBlockOption(s.currentSzx, s.currentNum, s.currentMore)
	if err != nil {
		return nil, err
	}

	req.SetOption(s.blockType, block)
	return req, nil
}

func (s *blockWiseSender) exchange(b *blockWiseSession, req Message) (Message, error) {
	var resp Message
	var err error
	if blockWiseDebug {
		log.Printf("sendPayload %p req=%v\n", b, req)
	}
	if s.peerDrive {
		resp, err = exchangeDrivedByPeer(b.networkSession, req, s.blockType)
	} else {
		resp, err = b.networkSession.Exchange(req)
	}
	if err != nil {
		return nil, err
	}
	if blockWiseDebug {
		log.Printf("sendPayload %p resp=%v\n", b, resp)
	}
	return resp, nil
}

func (s *blockWiseSender) processResp(b *blockWiseSession, req Message, resp Message) (Message, error) {
	if s.currentMore == false {
		if s.blockType == Block1 {
			if respBlock2, ok := resp.Option(Block2).(uint32); ok {
				szx, num, _, err := UnmarshalBlockOption(respBlock2)
				if err != nil {
					return nil, err
				}
				if !b.blockWiseIsValid(szx) {
					return nil, ErrInvalidBlockSzx
				}
				if num == 0 {
					resp.RemoveOption(s.sizeType())
					return b.receivePayload(s.peerDrive, s.origin, resp, Block2, s.origin.Code())
				}
			}
		}
		// clean response from blockWise staff
		if !s.peerDrive {
			resp.SetMessageID(s.origin.MessageID())
		}
		resp.RemoveOption(s.sizeType())
		resp.RemoveOption(s.blockType)
		return resp, nil
	}

	if resp.Code() != s.expectedCode {
		return resp, nil
	}

	if respBlock, ok := resp.Option(s.blockType).(uint32); ok {
		szx, num, _ /*more*/, err := UnmarshalBlockOption(respBlock)
		if err != nil {
			return nil, err
		}
		if !b.blockWiseIsValid(szx) {
			return nil, ErrInvalidBlockSzx
		}

		s.currentSzx = szx
		if s.peerDrive {
			s.currentNum = num
			req.SetMessageID(resp.MessageID())
		} else {
			s.currentNum = calcNextNum(num, szx, len(req.Payload()))
			req.SetMessageID(GenerateMessageID())
		}
		startOffset := calcStartOffset(s.currentNum, szx)
		endOffset := startOffset + b.blockWiseMaxPayloadSize(szx)
		if endOffset > len(s.origin.Payload()) {
			endOffset = len(s.origin.Payload())
			s.currentMore = false
		}
		req.SetPayload(s.origin.Payload()[startOffset:endOffset])
		//must be unique for evey msg via UDP
		if blockWiseDebug {
			log.Printf("sendPayload szx=%v num=%v more=%v\n", s.currentSzx, s.currentNum, s.currentMore)
		}
		block, err := MarshalBlockOption(s.currentSzx, s.currentNum, s.currentMore)
		if err != nil {
			return nil, err
		}
		req.SetOption(s.blockType, block)
	} else {
		switch s.blockType {
		case Block1:
			return nil, ErrInvalidOptionBlock1
		default:
			return nil, ErrInvalidOptionBlock2
		}
	}
	return nil, nil
}

func (b *blockWiseSession) sendPayload(peerDrive bool, blockType OptionID, suggestedSzx BlockSzx, expectedCode COAPCode, msg Message) (Message, error) {
	s := newSender(peerDrive, blockType, suggestedSzx, expectedCode, msg)
	req, err := s.createReq(b)
	if err != nil {
		return nil, err
	}
	for {
		bwResp, err := s.exchange(b, req)
		if err != nil {
			return nil, err
		}

		resp, err := s.processResp(b, req, bwResp)
		if err != nil {
			return nil, err
		}

		if resp != nil {
			return resp, nil
		}
	}
}

type blockWiseSession struct {
	networkSession
}

func (b *blockWiseSession) Exchange(msg Message) (Message, error) {
	switch msg.Code() {
	//these methods doesn't need to be handled by blockwise
	case CSM, Ping, Pong, Release, Abort, Empty:
		return b.networkSession.Exchange(msg)
	case GET, DELETE:
		return b.receivePayload(false, msg, nil, Block2, msg.Code())
	case POST, PUT:
		return b.sendPayload(false, Block1, b.networkSession.blockWiseSzx(), Continue, msg)
	// for response code
	default:
		return b.sendPayload(true, Block2, b.networkSession.blockWiseSzx(), Continue, msg)
	}

}

func (b *blockWiseSession) Write(msg Message) error {
	switch msg.Code() {
	case CSM, Ping, Pong, Release, Abort, Empty, GET:
		return b.networkSession.Write(msg)
	default:
		_, err := b.Exchange(msg)
		return err
	}
}

func calcNextNum(num uint, szx BlockSzx, payloadSize int) uint {
	val := uint(payloadSize / SZXVal[szx])
	if val > 0 && (payloadSize%SZXVal[szx] == 0) {
		val--
	}
	return num + val + 1
}

func calcStartOffset(num uint, szx BlockSzx) int {
	return int(num) * SZXVal[szx]
}

func (b *blockWiseSession) sendErrorMsg(code COAPCode, typ COAPType, token []byte, MessageID uint16) {
	req := b.NewMessage(MessageParams{
		Code:      code,
		Type:      typ,
		MessageID: MessageID,
		Token:     token,
	})
	b.networkSession.Write(req)
}

type blockWiseReceiver struct {
	peerDrive    bool
	code         COAPCode
	expectedCode COAPCode
	typ          COAPType
	origin       Message
	blockType    OptionID
	currentSzx   BlockSzx
	nextNum      uint
	currentMore  bool
	payloadSize  uint32

	payload *bytes.Buffer
}

func (r *blockWiseReceiver) sizeType() OptionID {
	if r.blockType == Block1 {
		return Size1
	}
	return Size2
}

func (r *blockWiseReceiver) coapType() COAPType {
	if r.peerDrive {
		return Acknowledgement
	}
	return Confirmable
}

func (r *blockWiseReceiver) createReq(b *blockWiseSession, resp Message) (Message, error) {
	req := b.networkSession.NewMessage(MessageParams{
		Code:      r.code,
		Type:      r.typ,
		MessageID: r.origin.MessageID(),
		Token:     r.origin.Token(),
	})
	if !r.peerDrive {
		for _, option := range r.origin.AllOptions() {
			req.AddOption(option.ID, option.Value)
		}
		req.SetMessageID(GenerateMessageID())
	} else if resp == nil {
		// set blocktype as peer wants
		block := r.origin.Option(r.blockType)
		if block != nil {
			req.SetOption(r.blockType, block)
		}
	}

	if r.payload.Len() > 0 {
		block, err := MarshalBlockOption(r.currentSzx, r.nextNum, r.currentMore)
		if err != nil {
			return nil, err
		}
		req.SetOption(r.blockType, block)
	}
	return req, nil
}

func newReceiver(b *blockWiseSession, peerDrive bool, origin Message, resp Message, blockType OptionID, code COAPCode) (r *blockWiseReceiver, res Message, err error) {
	r = &blockWiseReceiver{
		peerDrive:  peerDrive,
		code:       code,
		origin:     origin,
		blockType:  blockType,
		currentSzx: b.networkSession.blockWiseSzx(),
		payload:    bytes.NewBuffer(make([]byte, 0)),
	}

	if resp != nil {
		var ok bool
		if r.payloadSize, ok = resp.Option(r.sizeType()).(uint32); ok {
			//try to get Size
			r.payload.Grow(int(r.payloadSize))
		}
		if respBlock, ok := resp.Option(blockType).(uint32); ok {
			//contains block
			szx, num, more, err := UnmarshalBlockOption(respBlock)
			if err != nil {
				return r, nil, err
			}
			if !b.blockWiseIsValid(szx) {
				return r, nil, ErrInvalidBlockSzx
			}
			//do we need blockWise?
			if more == false {
				resp.RemoveOption(r.sizeType())
				resp.RemoveOption(blockType)
				if !peerDrive {
					resp.SetMessageID(origin.MessageID())
				}
				return r, resp, nil
			}
			//set szx and num by response
			r.currentSzx = szx
			r.nextNum = calcNextNum(num, r.currentSzx, len(resp.Payload()))
			r.currentMore = more
		} else {
			//it's doesn't contains block
			return r, resp, nil
		}
		//append payload and set block
		r.payload.Write(resp.Payload())
	}

	if peerDrive {
		//we got all message returns it to handler
		if respBlock, ok := origin.Option(blockType).(uint32); ok {
			szx, num, more, err := UnmarshalBlockOption(respBlock)
			if err != nil {
				return r, nil, err
			}
			if !b.blockWiseIsValid(szx) {
				return r, nil, ErrInvalidBlockSzx
			}
			if more == false {
				origin.RemoveOption(blockType)

				return r, origin, nil
			}
			r.currentSzx = szx
			r.nextNum = num
			r.currentMore = more
		}
		r.payload.Write(origin.Payload())
	}

	return r, nil, nil
}

func (r *blockWiseReceiver) exchange(b *blockWiseSession, req Message) (Message, error) {
	if blockWiseDebug {
		log.Printf("receivePayload %p req=%v\n", b, req)
	}
	var resp Message
	var err error
	if r.peerDrive {
		resp, err = exchangeDrivedByPeer(b.networkSession, req, r.blockType)
	} else {
		resp, err = b.networkSession.Exchange(req)
	}

	if blockWiseDebug {
		log.Printf("receivePayload %p resp=%v\n", b, resp)
	}

	return resp, err
}

func (r *blockWiseReceiver) processResp(b *blockWiseSession, req Message, resp Message) (Message, error) {
	if respBlock, ok := resp.Option(r.blockType).(uint32); ok {
		szx, num, more, err := UnmarshalBlockOption(respBlock)
		if err != nil {
			return nil, err
		}
		if !b.blockWiseIsValid(szx) {
			return nil, ErrInvalidBlockSzx
		}
		startOffset := calcStartOffset(num, szx)
		if r.payload.Len() < startOffset {
			return nil, ErrRequestEntityIncomplete
		}
		if more == true && len(resp.Payload())%SZXVal[szx] != 0 {
			if r.peerDrive {
				return nil, ErrInvalidRequest
			}
			//reagain
			r.nextNum = num
		} else {
			r.payload.Truncate(startOffset)
			r.payload.Write(resp.Payload())
			if r.peerDrive {
				r.nextNum = num
			} else {
				if szx > b.blockWiseSzx() {
					num = 0
					szx = b.blockWiseSzx()
					r.nextNum = calcNextNum(num, szx, r.payload.Len())
				} else {
					r.nextNum = calcNextNum(num, szx, len(resp.Payload()))
				}
			}
		}

		if more == false {
			if r.payloadSize != 0 && int(r.payloadSize) != r.payload.Len() {
				return nil, ErrInvalidPayloadSize
			}
			if r.payload.Len() > 0 {
				resp.SetPayload(r.payload.Bytes())
			}
			// remove block used by blockWise
			resp.RemoveOption(r.sizeType())
			resp.RemoveOption(r.blockType)
			if !r.peerDrive {
				resp.SetMessageID(r.origin.MessageID())
			}
			return resp, nil
		}
		if r.peerDrive {
			req.SetMessageID(resp.MessageID())
		} else {
			req.SetMessageID(GenerateMessageID())
		}
		if blockWiseDebug {
			log.Printf("receivePayload szx=%v num=%v more=%v\n", szx, r.nextNum, more)
		}
		block, err := MarshalBlockOption(szx, r.nextNum, more)
		if err != nil {
			return nil, err
		}
		req.SetOption(r.blockType, block)
	} else {
		if r.payloadSize != 0 && int(r.payloadSize) != len(resp.Payload()) {
			return nil, ErrInvalidPayloadSize
		}
		//response is whole doesn't need to use blockwise
		return resp, nil
	}
	return nil, nil
}

func (r *blockWiseReceiver) sendError(b *blockWiseSession, code COAPCode, resp Message) {
	var MessageID uint16
	var token []byte
	var typ COAPType
	if !r.peerDrive {
		MessageID = GenerateMessageID()
		token = r.origin.Token()
		typ = NonConfirmable
	} else {
		MessageID = r.origin.MessageID()
		typ = Acknowledgement
		if resp != nil {
			token = resp.Token()
		} else {
			token = r.origin.Token()
		}
	}
	b.sendErrorMsg(code, typ, token, MessageID)
}

func (b *blockWiseSession) receivePayload(peerDrive bool, msg Message, resp Message, blockType OptionID, code COAPCode) (Message, error) {
	r, resp, err := newReceiver(b, peerDrive, msg, resp, blockType, code)
	if err != nil {
		r.sendError(b, BadRequest, resp)
		return nil, err
	}
	if resp != nil {
		return resp, nil
	}

	req, err := r.createReq(b, resp)
	if err != nil {
		r.sendError(b, BadRequest, resp)
		return nil, err
	}

	for {
		bwResp, err := r.exchange(b, req)

		if err != nil {
			r.sendError(b, BadRequest, resp)
			return nil, err
		}

		resp, err := r.processResp(b, req, bwResp)

		if err != nil {
			errCode := BadRequest
			switch err {
			case ErrRequestEntityIncomplete:
				errCode = RequestEntityIncomplete
			}
			r.sendError(b, errCode, resp)
			return nil, err
		}

		if resp != nil {
			return resp, nil
		}
	}
}

func handleBlockWiseMsg(w ResponseWriter, r *Request, next func(w ResponseWriter, r *Request)) {
	if blockWiseDebug {
		fmt.Printf("handleBlockWiseMsg r.msg=%v\n", r.Msg)
	}
	if r.Msg.Token() != nil {
		switch r.Msg.Code() {
		case PUT, POST:
			if b, ok := r.Client.networkSession.(*blockWiseSession); ok {
				msg, err := b.receivePayload(true, r.Msg, nil, Block1, Continue)

				if err != nil {
					return
				}
				next(w, &Request{Client: r.Client, Msg: msg})
				return
			}
			/*
				//observe data
				case Content, Valid:
					if r.Msg.Option(Observe) != nil && r.Msg.Option(ETag) != nil {
						if b, ok := r.networkSession.(*blockWiseSession); ok {
							token, err := GenerateToken(8)
							if err != nil {
								return
							}
							req := r.networkSession.NewMessage(MessageParams{
								Code:      GET,
								Type:      Confirmable,
								MessageID: GenerateMessageID(),
								Token:     token,
							})
							req.AddOption(Block2, r.Msg.Option(Block2))
							req.AddOption(Size2, r.Msg.Option(Size2))

							msg, err := b.receivePayload(true, req, r.Msg, Block2, GET, r.Msg.Code())
							if err != nil {
								return
							}
							next(w, &Request{networkSession: r.networkSession, Msg: msg})
							return
						}
					}*/
		}

	}
	next(w, r)
}

type blockWiseResponseWriter struct {
	*responseWriter
}

func (w *blockWiseResponseWriter) Write(msg Message) error {
	suggestedSzx := w.req.Client.networkSession.blockWiseSzx()
	if respBlock2, ok := w.req.Msg.Option(Block2).(uint32); ok {
		szx, _, _, err := UnmarshalBlockOption(respBlock2)
		if err != nil {
			return err
		}
		//BERT is supported only via TCP
		if szx == BlockSzxBERT && !w.req.Client.networkSession.IsTCP() {
			return ErrInvalidBlockSzx
		}
		suggestedSzx = szx
	}

	//resp is less them szx then just write msg without blockWise
	if len(msg.Payload()) < SZXVal[suggestedSzx] {
		return w.responseWriter.Write(msg)
	}

	if b, ok := w.req.Client.networkSession.(*blockWiseSession); ok {
		_, err := b.sendPayload(true, Block2, suggestedSzx, w.req.Msg.Code(), msg)
		return err
	}

	return ErrNotSupported
}

type blockWiseNoticeWriter struct {
	*responseWriter
}

func (w *blockWiseNoticeWriter) Write(msg Message) error {
	suggestedSzx := w.req.Client.networkSession.blockWiseSzx()
	if respBlock2, ok := w.req.Msg.Option(Block2).(uint32); ok {
		szx, _, _, err := UnmarshalBlockOption(respBlock2)
		if err != nil {
			return err
		}
		//BERT is supported only via TCP
		if szx == BlockSzxBERT && !w.req.Client.networkSession.IsTCP() {
			return ErrInvalidBlockSzx
		}
		suggestedSzx = szx
	}

	//resp is less them szx then just write msg without blockWise
	if len(msg.Payload()) < SZXVal[suggestedSzx] {
		return w.responseWriter.Write(msg)
	}

	if b, ok := w.req.Client.networkSession.(*blockWiseSession); ok {
		s := newSender(false, Block2, suggestedSzx, w.req.Msg.Code(), msg)
		req, err := s.createReq(b)
		if err != nil {
			return err
		}
		return b.networkSession.Write(req)
	}
	return ErrNotSupported
}
