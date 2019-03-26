package coap

import (
	"bytes"
	"context"
	"fmt"
	"log"
)

const (
	maxBlockNumber = uint(1048575)
	blockWiseDebug = false
)

// BlockWiseSzx enum representation for szx
type BlockWiseSzx uint8

const (
	//BlockWiseSzx16 block of size 16bytes
	BlockWiseSzx16 BlockWiseSzx = 0
	//BlockWiseSzx32 block of size 32bytes
	BlockWiseSzx32 BlockWiseSzx = 1
	//BlockWiseSzx64 block of size 64bytes
	BlockWiseSzx64 BlockWiseSzx = 2
	//BlockWiseSzx128 block of size 128bytes
	BlockWiseSzx128 BlockWiseSzx = 3
	//BlockWiseSzx256 block of size 256bytes
	BlockWiseSzx256 BlockWiseSzx = 4
	//BlockWiseSzx512 block of size 512bytes
	BlockWiseSzx512 BlockWiseSzx = 5
	//BlockWiseSzx1024 block of size 1024bytes
	BlockWiseSzx1024 BlockWiseSzx = 6
	//BlockWiseSzxBERT block of size n*1024bytes
	BlockWiseSzxBERT BlockWiseSzx = 7

	//BlockWiseSzxCount count of block enums
	BlockWiseSzxCount BlockWiseSzx = 8
)

var szxToBytes = [BlockWiseSzxCount]int{
	BlockWiseSzx16:   16,
	BlockWiseSzx32:   32,
	BlockWiseSzx64:   64,
	BlockWiseSzx128:  128,
	BlockWiseSzx256:  256,
	BlockWiseSzx512:  512,
	BlockWiseSzx1024: 1024,
	BlockWiseSzxBERT: 1024, //for calculate size of block
}

func MarshalBlockOption(szx BlockWiseSzx, blockNumber uint, moreBlocksFollowing bool) (uint32, error) {
	if szx >= BlockWiseSzxCount {
		return 0, ErrInvalidBlockWiseSzx
	}
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

func UnmarshalBlockOption(blockVal uint32) (szx BlockWiseSzx, blockNumber uint, moreBlocksFollowing bool, err error) {
	if blockVal > 0xffffff {
		err = ErrBlockInvalidSize
	}

	szx = BlockWiseSzx(blockVal & 0x7) //masking for the SZX
	if (blockVal & 0x8) != 0 {         //masking for the "M"
		moreBlocksFollowing = true
	}
	blockNumber = uint(blockVal) >> 4 //shifting out the SZX and M vals. leaving the block number behind
	if blockNumber > maxBlockNumber {
		err = ErrBlockNumberExceedLimit
	}
	return
}

func exchangeDrivedByPeer(ctx context.Context, session networkSession, req Message, blockType OptionID) (Message, error) {
	if block, ok := req.Option(blockType).(uint32); ok {
		_, _, more, err := UnmarshalBlockOption(block)
		if err != nil {
			return nil, err
		}
		if more == false {
			// we send all datas to peer -> create empty response
			err := session.WriteMsgWithContext(ctx, req)
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
	err := session.WriteMsgWithContext(ctx, req)
	if err != nil {
		return nil, err
	}
	select {
	case resp := <-pair:
		return resp.Msg, nil
	case <-ctx.Done():
		if ctx.Err() != nil {
			return nil, fmt.Errorf("cannot exchange drived by peer: %v", err)
		}
		return nil, ErrTimeout
	}
}

type blockWiseSender struct {
	peerDrive    bool
	blockType    OptionID
	expectedCode COAPCode
	origin       Message

	currentNum  uint
	currentSzx  BlockWiseSzx
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

func newSender(peerDrive bool, blockType OptionID, suggestedSzx BlockWiseSzx, expectedCode COAPCode, origin Message) *blockWiseSender {
	return &blockWiseSender{
		peerDrive:    peerDrive,
		blockType:    blockType,
		currentSzx:   suggestedSzx,
		expectedCode: expectedCode,
		origin:       origin,
	}
}

func (s *blockWiseSender) newReq(b *blockWiseSession) (Message, error) {
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
	var maxPayloadSize int
	maxPayloadSize, s.currentSzx = b.blockWiseMaxPayloadSize(s.currentSzx)
	if s.origin.Payload() != nil && len(s.origin.Payload()) > maxPayloadSize {
		req.SetPayload(s.origin.Payload()[:maxPayloadSize])
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

func (s *blockWiseSender) exchange(ctx context.Context, b *blockWiseSession, req Message) (Message, error) {
	var resp Message
	var err error
	if blockWiseDebug {
		log.Printf("sendPayload %p req=%v\n", b, req)
	}
	if s.peerDrive {
		resp, err = exchangeDrivedByPeer(ctx, b.networkSession, req, s.blockType)
	} else {
		resp, err = b.networkSession.ExchangeWithContext(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	if blockWiseDebug {
		log.Printf("sendPayload %p resp=%v\n", b, resp)
	}
	return resp, nil
}

func (s *blockWiseSender) processResp(ctx context.Context, b *blockWiseSession, req Message, resp Message) (Message, error) {
	if s.currentMore == false {
		if s.blockType == Block1 {
			if respBlock2, ok := resp.Option(Block2).(uint32); ok {
				szx, num, _, err := UnmarshalBlockOption(respBlock2)
				if err != nil {
					return nil, err
				}
				if !b.blockWiseIsValid(szx) {
					return nil, ErrInvalidBlockWiseSzx
				}
				if num == 0 {
					resp.RemoveOption(s.sizeType())
					return b.receivePayload(ctx, s.peerDrive, s.origin, resp, Block2, s.origin.Code())
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
		return resp, ErrUnexpectedReponseCode
	}

	if respBlock, ok := resp.Option(s.blockType).(uint32); ok {
		szx, num, _ /*more*/, err := UnmarshalBlockOption(respBlock)
		if err != nil {
			return nil, err
		}
		if !b.blockWiseIsValid(szx) {
			return nil, ErrInvalidBlockWiseSzx
		}

		var maxPayloadSize int
		maxPayloadSize, s.currentSzx = b.blockWiseMaxPayloadSize(szx)
		if s.peerDrive {
			s.currentNum = num
			req.SetMessageID(resp.MessageID())
		} else {
			s.currentNum = calcNextNum(num, szx, len(req.Payload()))
			req.SetMessageID(GenerateMessageID())
		}
		startOffset := calcStartOffset(s.currentNum, szx)
		endOffset := startOffset + maxPayloadSize
		if endOffset >= len(s.origin.Payload()) {
			endOffset = len(s.origin.Payload())
			s.currentMore = false
		}
		if startOffset > len(s.origin.Payload()) {
			return nil, ErrBlockInvalidSize
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

func (b *blockWiseSession) sendPayload(ctx context.Context, peerDrive bool, blockType OptionID, suggestedSzx BlockWiseSzx, expectedCode COAPCode, msg Message) (Message, error) {
	s := newSender(peerDrive, blockType, suggestedSzx, expectedCode, msg)
	req, err := s.newReq(b)
	if err != nil {
		return nil, err
	}
	for {
		bwResp, err := s.exchange(ctx, b, req)
		if err != nil {
			return nil, err
		}

		resp, err := s.processResp(ctx, b, req, bwResp)
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
	return b.ExchangeWithContext(context.Background(), msg)
}

func (b *blockWiseSession) ExchangeWithContext(ctx context.Context, msg Message) (Message, error) {
	switch msg.Code() {
	//these methods doesn't need to be handled by blockwise
	case CSM, Ping, Pong, Release, Abort, Empty:
		return b.networkSession.ExchangeWithContext(ctx, msg)
	case GET, DELETE:
		return b.receivePayload(ctx, false, msg, nil, Block2, msg.Code())
	case POST, PUT:
		return b.sendPayload(ctx, false, Block1, b.networkSession.blockWiseSzx(), Continue, msg)
	// for response code
	default:
		return b.sendPayload(ctx, true, Block2, b.networkSession.blockWiseSzx(), Continue, msg)
	}

}

func (b *blockWiseSession) WriteMsg(msg Message) error {
	return b.WriteMsgWithContext(context.Background(), msg)
}

func (b *blockWiseSession) validateMessageSize(msg Message) error {
	size, err := msg.ToBytesLength()
	if err != nil {
		return err
	}
	session, ok := b.networkSession.(*sessionTCP)
	if !ok {
		// Not supported for UDP session
		return nil
	}

	if session.peerMaxMessageSize != 0 &&
		uint32(size) > session.peerMaxMessageSize {
		return ErrMaxMessageSizeLimitExceeded
	}

	return nil
}

func (b *blockWiseSession) WriteMsgWithContext(ctx context.Context, msg Message) error {
	if err := b.validateMessageSize(msg); err != nil {
		return err
	}
	switch msg.Code() {
	case CSM, Ping, Pong, Release, Abort, Empty, GET:
		return b.networkSession.WriteMsgWithContext(ctx, msg)
	default:
		_, err := b.ExchangeWithContext(ctx, msg)
		return err
	}
}

func calcNextNum(num uint, szx BlockWiseSzx, payloadSize int) uint {
	val := uint(payloadSize / szxToBytes[szx])
	if val > 0 && (payloadSize%szxToBytes[szx] == 0) {
		val--
	}
	return num + val + 1
}

func calcStartOffset(num uint, szx BlockWiseSzx) int {
	return int(num) * szxToBytes[szx]
}

func (b *blockWiseSession) sendErrorMsg(ctx context.Context, code COAPCode, typ COAPType, token []byte, MessageID uint16, err error) {
	req := b.NewMessage(MessageParams{
		Code:      code,
		Type:      typ,
		MessageID: MessageID,
		Token:     token,
	})
	if err != nil {
		req.SetOption(ContentFormat, TextPlain)
		req.SetPayload([]byte(err.Error()))
	}
	b.networkSession.WriteMsgWithContext(ctx, req)
}

type blockWiseReceiver struct {
	peerDrive    bool
	code         COAPCode
	expectedCode COAPCode
	typ          COAPType
	origin       Message
	blockType    OptionID
	currentSzx   BlockWiseSzx
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

func (r *blockWiseReceiver) newReq(b *blockWiseSession, resp Message) (Message, error) {
	req := b.networkSession.NewMessage(MessageParams{
		Code:      r.code,
		Type:      r.typ,
		MessageID: r.origin.MessageID(),
		Token:     r.origin.Token(),
	})
	if !r.peerDrive {
		for _, option := range r.origin.AllOptions() {
			//dont send content format when we receiving payload
			if option.ID != ContentFormat {
				req.AddOption(option.ID, option.Value)
			}
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
				return r, nil, ErrInvalidBlockWiseSzx
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
				return r, nil, ErrInvalidBlockWiseSzx
			}
			if more == false {
				origin.RemoveOption(blockType)

				return r, origin, nil
			}
			r.currentSzx = szx
			r.nextNum = num
			r.currentMore = more
			r.payload.Write(origin.Payload())
		} else {
			//peerdrive doesn't inform us that it wants to use blockwise - return original message
			return r, origin, nil
		}
	}

	return r, nil, nil
}

func (r *blockWiseReceiver) exchange(ctx context.Context, b *blockWiseSession, req Message) (Message, error) {
	if blockWiseDebug {
		log.Printf("receivePayload %p req=%v\n", b, req)
	}
	var resp Message
	var err error
	if r.peerDrive {
		resp, err = exchangeDrivedByPeer(ctx, b.networkSession, req, r.blockType)
	} else {
		resp, err = b.networkSession.ExchangeWithContext(ctx, req)
	}

	if blockWiseDebug {
		log.Printf("receivePayload %p resp=%v\n", b, resp)
	}

	return resp, err
}

func (r *blockWiseReceiver) validateMessageSize(msg Message, b *blockWiseSession) error {
	size, err := msg.ToBytesLength()
	if err != nil {
		return err
	}

	session, ok := b.networkSession.(*sessionTCP)
	if ok {
		if session.srv.MaxMessageSize != 0 &&
			uint32(size) > session.srv.MaxMessageSize {
			return ErrMaxMessageSizeLimitExceeded
		}
	}
	return nil
}

func (r *blockWiseReceiver) processResp(b *blockWiseSession, req Message, resp Message) (Message, error) {
	if err := r.validateMessageSize(req, b); err != nil {
		return nil, err
	}
	if respBlock, ok := resp.Option(r.blockType).(uint32); ok {
		szx, num, more, err := UnmarshalBlockOption(respBlock)
		if err != nil {
			return nil, err
		}
		if !b.blockWiseIsValid(szx) {
			return nil, ErrInvalidBlockWiseSzx
		}
		startOffset := calcStartOffset(num, szx)
		if r.payload.Len() < startOffset {
			return nil, ErrRequestEntityIncomplete
		}
		if more == true && len(resp.Payload())%szxToBytes[szx] != 0 {
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

func (r *blockWiseReceiver) sendError(ctx context.Context, b *blockWiseSession, code COAPCode, resp Message, err error) {
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
	b.sendErrorMsg(ctx, code, typ, token, MessageID, err)
}

func (b *blockWiseSession) receivePayload(ctx context.Context, peerDrive bool, msg Message, resp Message, blockType OptionID, code COAPCode) (Message, error) {
	r, resp, err := newReceiver(b, peerDrive, msg, resp, blockType, code)
	if err != nil {
		r.sendError(ctx, b, BadRequest, resp, err)
		return nil, err
	}
	if resp != nil {
		return resp, nil
	}

	req, err := r.newReq(b, resp)
	if err != nil {
		r.sendError(ctx, b, BadRequest, resp, err)
		return nil, err
	}

	for {
		bwResp, err := r.exchange(ctx, b, req)

		if err != nil {
			r.sendError(ctx, b, BadRequest, resp, err)
			return nil, err
		}

		resp, err := r.processResp(b, req, bwResp)

		if err != nil {
			errCode := BadRequest
			switch err {
			case ErrRequestEntityIncomplete:
				errCode = RequestEntityIncomplete
			}
			r.sendError(ctx, b, errCode, resp, err)
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
			if b, ok := r.Client.networkSession().(*blockWiseSession); ok {
				msg, err := b.receivePayload(r.Ctx, true, r.Msg, nil, Block1, Continue)

				if err != nil {
					return
				}

				// We need to be careful to create a new response writer for the
				// new request, otherwise the server may attempt to respond to
				// the wrong request.
				newReq := &Request{Client: r.Client, Msg: msg, Ctx: r.Ctx, Sequence: r.Client.Sequence()}
				newWriter := responseWriterFromRequest(newReq)
				next(newWriter, newReq)
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
							next(w, &Request{networkSession: r.networkSession, Msg: msg, Ctx: r.Ctx})
							return
						}
					}*/
		}

	}
	next(w, r)
}

type blockWiseResponseWriter struct {
	responseWriter ResponseWriter
}

func (w *blockWiseResponseWriter) NewResponse(code COAPCode) Message {
	return w.responseWriter.NewResponse(code)
}

func (w *blockWiseResponseWriter) SetCode(code COAPCode) {
	w.responseWriter.SetCode(code)
}

func (w *blockWiseResponseWriter) SetContentFormat(contentFormat MediaType) {
	w.responseWriter.SetContentFormat(contentFormat)
}

func (w *blockWiseResponseWriter) getCode() *COAPCode {
	return w.responseWriter.getCode()
}

func (w *blockWiseResponseWriter) getReq() *Request {
	return w.responseWriter.getReq()
}

func (w *blockWiseResponseWriter) getContentFormat() *MediaType {
	return w.responseWriter.getContentFormat()
}

//WriteMsg send whole message if size of payload is less then block szx otherwise
//send message via blockwise.
func (w *blockWiseResponseWriter) WriteMsg(msg Message) error {
	return w.WriteMsgWithContext(context.Background(), msg)
}

//Write send whole message with context if size of payload is less then block szx otherwise
//send message via blockwise.
func (w *blockWiseResponseWriter) WriteMsgWithContext(ctx context.Context, msg Message) error {
	suggestedSzx := w.responseWriter.getReq().Client.networkSession().blockWiseSzx()
	if respBlock2, ok := w.responseWriter.getReq().Msg.Option(Block2).(uint32); ok {
		szx, _, _, err := UnmarshalBlockOption(respBlock2)
		if err != nil {
			return err
		}
		//BERT is supported only via TCP
		if szx == BlockWiseSzxBERT && !w.responseWriter.getReq().Client.networkSession().IsTCP() {
			return ErrInvalidBlockWiseSzx
		}
		suggestedSzx = szx
	}

	//resp is less them szx then just write msg without blockWise
	if len(msg.Payload()) < szxToBytes[suggestedSzx] {
		return w.responseWriter.WriteMsgWithContext(ctx, msg)
	}

	if b, ok := w.responseWriter.getReq().Client.networkSession().(*blockWiseSession); ok {
		_, err := b.sendPayload(ctx, true, Block2, suggestedSzx, w.responseWriter.getReq().Msg.Code(), msg)
		return err
	}

	return ErrNotSupported
}

// Write send response to peer
func (w *blockWiseResponseWriter) Write(p []byte) (n int, err error) {
	return w.WriteWithContext(context.Background(), p)
}

// WriteContext send response with context to peer
func (w *blockWiseResponseWriter) WriteWithContext(ctx context.Context, p []byte) (n int, err error) {
	l, resp := prepareReponse(w, w.responseWriter.getReq().Msg.Code(), w.responseWriter.getCode(), w.responseWriter.getContentFormat(), p)
	err = w.WriteMsgWithContext(ctx, resp)
	return l, err
}

type blockWiseNoticeWriter struct {
	responseWriter ResponseWriter
}

func (w *blockWiseNoticeWriter) NewResponse(code COAPCode) Message {
	return w.responseWriter.NewResponse(code)
}

func (w *blockWiseNoticeWriter) SetCode(code COAPCode) {
	w.responseWriter.SetCode(code)
}

func (w *blockWiseNoticeWriter) SetContentFormat(contentFormat MediaType) {
	w.responseWriter.SetContentFormat(contentFormat)
}

func (w *blockWiseNoticeWriter) getCode() *COAPCode {
	return w.responseWriter.getCode()
}

func (w *blockWiseNoticeWriter) getReq() *Request {
	return w.responseWriter.getReq()
}

func (w *blockWiseNoticeWriter) getContentFormat() *MediaType {
	return w.responseWriter.getContentFormat()
}

func (w *blockWiseNoticeWriter) WriteMsg(msg Message) error {
	return w.WriteMsgWithContext(context.Background(), msg)
}

//Write send whole message with context. If size of payload is less then block szx otherwise
//send only first block. For Get whole msg client must call Get to
//resource.
func (w *blockWiseNoticeWriter) WriteMsgWithContext(ctx context.Context, msg Message) error {
	suggestedSzx := w.responseWriter.getReq().Client.networkSession().blockWiseSzx()
	if respBlock2, ok := w.responseWriter.getReq().Msg.Option(Block2).(uint32); ok {
		szx, _, _, err := UnmarshalBlockOption(respBlock2)
		if err != nil {
			return err
		}
		//BERT is supported only via TCP
		if szx == BlockWiseSzxBERT && !w.responseWriter.getReq().Client.networkSession().IsTCP() {
			return ErrInvalidBlockWiseSzx
		}
		suggestedSzx = szx
	}

	//resp is less them szx then just write msg without blockWise
	if len(msg.Payload()) < szxToBytes[suggestedSzx] {
		return w.responseWriter.WriteMsgWithContext(ctx, msg)
	}

	if b, ok := w.responseWriter.getReq().Client.networkSession().(*blockWiseSession); ok {
		s := newSender(false, Block2, suggestedSzx, w.responseWriter.getReq().Msg.Code(), msg)
		req, err := s.newReq(b)
		if err != nil {
			return err
		}
		return b.networkSession.WriteMsgWithContext(ctx, req)
	}
	return ErrNotSupported
}

// Write send response to peer
func (w *blockWiseNoticeWriter) Write(p []byte) (n int, err error) {
	return w.WriteWithContext(context.Background(), p)
}

// Write send response with context to peer
func (w *blockWiseNoticeWriter) WriteWithContext(ctx context.Context, p []byte) (n int, err error) {
	l, resp := prepareReponse(w, w.responseWriter.getReq().Msg.Code(), w.responseWriter.getCode(), w.responseWriter.getContentFormat(), p)
	err = w.WriteMsgWithContext(ctx, resp)
	return l, err
}
