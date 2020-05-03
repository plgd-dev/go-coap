package blockwise

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/patrickmn/go-cache"
)

const (
	maxBlockNumber = int(1048575)
)

// SZX enum representation for szx
type SZX uint8

// Size number of bytes.
func (s SZX) Size() int {
	switch s {
	case SZX16:
		return 16
	case SZX32:
		return 32
	case SZX64:
		return 64
	case SZX128:
		return 128
	case SZX256:
		return 256
	case SZX512:
		return 512
	case SZX1024, SZXBERT:
		return 1024
	}
	return -1
}

const (
	//SZX16 block of size 16bytes
	SZX16 SZX = 0
	//SZX32 block of size 32bytes
	SZX32 SZX = 1
	//SZX64 block of size 64bytes
	SZX64 SZX = 2
	//SZX128 block of size 128bytes
	SZX128 SZX = 3
	//SZX256 block of size 256bytes
	SZX256 SZX = 4
	//SZX512 block of size 512bytes
	SZX512 SZX = 5
	//SZX1024 block of size 1024bytes
	SZX1024 SZX = 6
	//SZXBERT block of size n*1024bytes
	SZXBERT SZX = 7
)

// ResponseWriter defines response interface for blockwise transfer.
type ResponseWriter interface {
	Message() Message
	SetMessage(Message)
}

type ROMessage interface {
	Context() context.Context
	Code() codes.Code
	Token() message.Token
	GetOptionUint32(id message.OptionID) (uint32, error)
	GetOptionBytes(id message.OptionID) ([]byte, error)
	Options() message.Options
	Payload() io.ReadSeeker
	PayloadSize() (int64, error)
}

// Message defines message interface for blockwise transfer.
type Message interface {
	ROMessage
	SetCode(codes.Code)
	SetToken(message.Token)
	SetOptionUint32(id message.OptionID, value uint32)
	Remove(id message.OptionID)
	SetOptions(message.Options)
	SetPayload(r io.ReadSeeker)
}

// EncodeBlockOption encodes block values to coap option.
func EncodeBlockOption(szx SZX, blockNumber int, moreBlocksFollowing bool) (uint32, error) {
	if szx > SZXBERT {
		return 0, ErrInvalidSZX
	}
	if blockNumber < 0 {
		return 0, ErrBlockNumberExceedLimit
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

// DecodeBlockOption decodes coap block option to block values.
func DecodeBlockOption(blockVal uint32) (szx SZX, blockNumber int, moreBlocksFollowing bool, err error) {
	if blockVal > 0xffffff {
		err = ErrBlockInvalidSize
	}

	szx = SZX(blockVal & 0x7)  //masking for the SZX
	if (blockVal & 0x8) != 0 { //masking for the "M"
		moreBlocksFollowing = true
	}
	blockNumber = int(blockVal) >> 4 //shifting out the SZX and M vals. leaving the block number behind
	if blockNumber > maxBlockNumber {
		err = ErrBlockNumberExceedLimit
	}
	return
}

type BlockWise struct {
	acquireMessage              func(ctx context.Context) Message
	releaseMessage              func(Message)
	receivingMessagesCache      *cache.Cache
	sendingMessagesCache        *cache.Cache
	errors                      func(error)
	autoCleanUpResponseCache    bool
	getSendedRequestFromOutside func(token message.Token) (Message, bool)
	bwSendedRequest             *sync.Map
}

type messageGuard struct {
	sync.Mutex
	request Message
}

func newRequestGuard(request Message) *messageGuard {
	return &messageGuard{
		request: request,
	}
}

func NewBlockWise(
	acquireMessage func(ctx context.Context) Message,
	releaseMessage func(Message),
	expiration time.Duration,
	errors func(error),
	autoCleanUpResponseCache bool,
	getSendedRequestFromOutside func(token message.Token) (Message, bool),
) *BlockWise {
	receivingMessagesCache := cache.New(expiration, expiration)
	bwSendedRequest := new(sync.Map)
	receivingMessagesCache.OnEvicted(func(tokenstr string, rece interface{}) {
		bwSendedRequest.Delete(tokenstr)
	})
	if getSendedRequestFromOutside == nil {
		getSendedRequestFromOutside = func(token message.Token) (Message, bool) { return nil, false }
	}
	return &BlockWise{
		acquireMessage:              acquireMessage,
		releaseMessage:              releaseMessage,
		receivingMessagesCache:      receivingMessagesCache,
		sendingMessagesCache:        cache.New(expiration, expiration),
		errors:                      errors,
		autoCleanUpResponseCache:    autoCleanUpResponseCache,
		getSendedRequestFromOutside: getSendedRequestFromOutside,
		bwSendedRequest:             bwSendedRequest,
	}
}

func bufferSize(szx SZX, maxMessageSize int) int {
	if szx < SZXBERT {
		return szx.Size()
	}
	return (maxMessageSize / szx.Size()) * szx.Size()
}

// Do sends an coap request and returns an coap response via blockwise transfer.
func (b *BlockWise) Do(r Message, maxSzx SZX, maxMessageSize int, do func(req Message) (Message, error)) (Message, error) {
	if maxSzx > SZXBERT {
		return nil, fmt.Errorf("invalid szx")
	}
	if len(r.Token()) == 0 {
		return nil, fmt.Errorf("invalid token")
	}

	blockID := message.Block2
	sizeID := message.Size2
	switch r.Code() {
	case codes.POST, codes.PUT:
		blockID = message.Block1
		sizeID = message.Size1
	}

	req := b.acquireMessage(r.Context())
	defer b.releaseMessage(req)
	req.SetCode(r.Code())
	req.SetToken(r.Token())
	req.SetOptions(r.Options())
	tokenStr := r.Token().String()
	b.bwSendedRequest.Store(tokenStr, req)
	defer b.bwSendedRequest.Delete(tokenStr)

	if r.Payload() == nil {
		resp, err := do(r)
		return resp, err
	}
	payloadSize, err := r.PayloadSize()
	if err != nil {
		return nil, fmt.Errorf("cannot get size of payload: %w", err)
	}

	if payloadSize <= int64(maxSzx.Size()) {
		return do(r)
	}
	req.SetOptionUint32(sizeID, uint32(payloadSize))

	num := 0
	buf := make([]byte, 1024)
	szx := maxSzx
	for {
		newBufLen := bufferSize(szx, maxMessageSize)
		if cap(buf) < newBufLen {
			buf = make([]byte, newBufLen)
		}
		buf = buf[:newBufLen]

		off := int64(num * szx.Size())
		newOff, err := r.Payload().Seek(off, io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("cannot seek in payload: %w", err)
		}
		readed, err := io.ReadFull(r.Payload(), buf)
		if err == io.ErrUnexpectedEOF {
			if newOff+int64(readed) == payloadSize {
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read payload: %w", err)
		}
		buf = buf[:readed]
		req.SetPayload(bytes.NewReader(buf))
		more := true
		if newOff+int64(readed) == payloadSize {
			more = false
		}
		block, err := EncodeBlockOption(szx, num, more)
		if err != nil {
			return nil, fmt.Errorf("cannot encode block option(%v, %v, %v) to bw request: %w", szx, num, more, err)
		}
		fmt.Printf("Do: block option(%v, %v, %v), payloadLen %v\n", szx, num, more, readed)

		req.SetOptionUint32(blockID, block)
		resp, err := do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot do bw request: %w", err)
		}
		fmt.Printf("Do: resp %v\n", resp)
		if resp.Code() != codes.Continue {
			return resp, nil
		}
		block, err = resp.GetOptionUint32(blockID)
		if err != nil {
			return resp, nil
		}
		szx, num, _, err = DecodeBlockOption(block)
		if err != nil {
			return resp, fmt.Errorf("cannot decode block option of bw response: %w", err)
		}
		fmt.Printf("Do: block decoded option(%v, %v)\n", szx, num)
	}
}

type writeRequestResponse struct {
	request        Message
	releaseMessage func(Message)
}

func NewWriteRequestResponse(request Message, acquireMessage func(context.Context) Message, releaseMessage func(Message)) *writeRequestResponse {
	req := acquireMessage(request.Context())
	req.SetCode(request.Code())
	req.SetToken(request.Token())
	req.SetOptions(request.Options())
	req.SetPayload(request.Payload())
	return &writeRequestResponse{
		request:        req,
		releaseMessage: releaseMessage,
	}
}

func (w *writeRequestResponse) SetMessage(r Message) {
	w.releaseMessage(w.request)
	w.request = r
}

func (w *writeRequestResponse) Message() Message {
	return w.request
}

// WriteRequest sends an coap request via blockwise transfer.
func (b *BlockWise) WriteRequest(request Message, maxSZX SZX, maxMessageSize int, writeRequest func(r Message) error) error {
	req := b.acquireMessage(request.Context())
	req.SetCode(request.Code())
	req.SetToken(request.Token())
	req.SetOptions(request.Options())
	tokenStr := request.Token().String()
	b.bwSendedRequest.Store(tokenStr, req)
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}

	w := NewWriteRequestResponse(request, b.acquireMessage, b.releaseMessage)
	err = b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock)
	if err != nil {
		return fmt.Errorf("cannot start writing request: %w", err)
	}
	return writeRequest(w.Message())
}

func fitSZX(r Message, blockType message.OptionID, maxSZX SZX) SZX {
	block, err := r.GetOptionUint32(blockType)
	if err == nil {
		szx, _, _, err := DecodeBlockOption(block)
		if err != nil {
			if maxSZX > szx {
				return szx
			}
		}
	}
	return maxSZX
}

func (b *BlockWise) handleSendingMessage(w ResponseWriter, sendingMessage Message, maxSZX SZX, maxMessageSize int, token []byte, block uint32) (bool, error) {
	blockType := message.Block2
	sizeType := message.Size2
	switch sendingMessage.Code() {
	case codes.POST, codes.PUT:
		blockType = message.Block1
		sizeType = message.Size1
	}

	szx, num, _, err := DecodeBlockOption(block)
	if err != nil {
		return false, fmt.Errorf("cannot decode %v option: %w", blockType, err)
	}
	off := int64(num * szx.Size())
	if szx > maxSZX {
		szx = maxSZX
	}
	sendMessage := b.acquireMessage(sendingMessage.Context())
	sendMessage.SetCode(sendingMessage.Code())
	sendMessage.SetOptions(sendingMessage.Options())
	sendMessage.SetToken(token)
	payloadSize, err := sendingMessage.PayloadSize()
	if err != nil {
		return false, fmt.Errorf("cannot get size of payload: %w", err)
	}
	offSeek, err := sendingMessage.Payload().Seek(off, io.SeekStart)
	if err != nil {
		return false, fmt.Errorf("cannot seek in response: %w", err)
	}
	if off != offSeek {
		return false, fmt.Errorf("cannot seek to requested offset(%v != %v)", off, offSeek)
	}
	buf := make([]byte, 1024)
	newBufLen := bufferSize(szx, maxMessageSize)
	if len(buf) < newBufLen {
		buf = make([]byte, newBufLen)
	}
	buf = buf[:newBufLen]

	readed, err := io.ReadFull(sendingMessage.Payload(), buf)
	if err == io.ErrUnexpectedEOF {
		if offSeek+int64(readed) == payloadSize {
			err = nil
		}
	}

	buf = buf[:readed]
	sendMessage.SetPayload(bytes.NewReader(buf))
	more := true
	if offSeek+int64(readed) == payloadSize {
		more = false
	}
	sendMessage.SetOptionUint32(sizeType, uint32(payloadSize))
	num = (int(offSeek)+readed)/szx.Size() - (readed / szx.Size())
	block, err = EncodeBlockOption(szx, num, more)
	if err != nil {
		return false, fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	sendMessage.SetOptionUint32(blockType, block)
	fmt.Printf("%p handleSendingMessage (%v, %v, %v)\n", b, szx, num, more)
	w.SetMessage(sendMessage)
	etag, _ := sendingMessage.GetOptionBytes(message.ETag)
	fmt.Printf("%p handleSendingMessage sendingMessage ETAG %v\n", b, etag)
	etag, _ = sendMessage.GetOptionBytes(message.ETag)
	fmt.Printf("%p handleSendingMessage ETAG %v\n", b, etag)
	return more, nil
}

// RemoveFromResponseCache removes response from cache. It need's tu be used for udp coap.
func (b *BlockWise) RemoveFromResponseCache(token message.Token) {
	if len(token) == 0 {
		return
	}
	b.sendingMessagesCache.Delete(token.String())
}

func (b *BlockWise) sendReset(w ResponseWriter) {
	sendMessage := b.acquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.Empty)
	sendMessage.SetToken(w.Message().Token())
	w.SetMessage(sendMessage)
}

// Handle middleware which constructs COAP request from blockwise transfer and send COAP response via blockwise.
func (b *BlockWise) Handle(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, next func(w ResponseWriter, r Message)) {
	if maxSZX > SZXBERT {
		panic("invalid maxSZX")
	}
	token := r.Token()
	fmt.Printf("%p Handle %v\n", b, r)
	if len(token) == 0 {
		err := b.handleReceivedMessage(w, r, maxSZX, maxMessageSize, next)
		if err != nil {
			b.sendReset(w)
			b.errors(fmt.Errorf("handleReceivedMessage: %w", err))
		}
		return
	}
	tokenStr := token.String()
	v, ok := b.sendingMessagesCache.Get(tokenStr)
	fmt.Printf("%p Handle.sendingMessagesCache %v %v\n", b, v, ok)
	if !ok {
		err := b.handleReceivedMessage(w, r, maxSZX, maxMessageSize, next)
		fmt.Printf("%p Handle.handleReceivedMessage %v\n", b, err)
		if err != nil {
			b.sendReset(w)
			b.errors(fmt.Errorf("handleReceivedMessage: %w", err))
		}
		return
	}
	more, err := b.continueSendingMessage(w, r, maxSZX, maxMessageSize, v.(*messageGuard))
	if err != nil {
		b.sendingMessagesCache.Delete(tokenStr)
		b.sendReset(w)
		b.errors(fmt.Errorf("continueSendingMessage: %w", err))
		return
	}
	if b.autoCleanUpResponseCache && more == false {
		b.RemoveFromResponseCache(token)
	}
}

func (b *BlockWise) handleReceivedMessage(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, next func(w ResponseWriter, r Message)) error {
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	switch r.Code() {
	case codes.CSM, codes.Ping, codes.Pong, codes.Release, codes.Abort, codes.Empty, codes.Continue:
		next(w, r)
		return nil
	case codes.GET, codes.DELETE:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		block, err := r.GetOptionUint32(message.Block2)
		if err == nil {
			r.Remove(message.Block2)
		}
		next(w, r)
		if w.Message().Code() == codes.Content && err == nil {
			startSendingMessageBlock = block
		}
	case codes.POST, codes.PUT:
		maxSZX = fitSZX(r, message.Block1, maxSZX)
		err := b.processReceivedMessage(w, r, maxSZX, next, message.Block1, message.Size1)
		if err != nil {
			return err
		}
	default:
		maxSZX = fitSZX(r, message.Block2, maxSZX)

		err = b.processReceivedMessage(w, r, maxSZX, next, message.Block2, message.Size2)
		if err != nil {
			return err
		}

	}
	return b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock)
}

func (b *BlockWise) continueSendingMessage(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, messageGuard *messageGuard) (bool, error) {
	messageGuard.Lock()
	defer messageGuard.Unlock()
	resp := messageGuard.request
	blockType := message.Block2
	switch resp.Code() {
	case codes.POST, codes.PUT:
		blockType = message.Block1
	}

	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		return false, fmt.Errorf("cannot get block2 option: %w", err)
	}
	more, err := b.handleSendingMessage(w, resp, maxSZX, maxMessageSize, r.Token(), block)
	fmt.Printf("%p Handle.handleSendingMessage %v\n", b, err)
	if err != nil {
		return false, fmt.Errorf("handleSendingMessage: %w", err)
	}
	return more, err
}

func isObserveResponse(msg Message) bool {
	_, err := msg.GetOptionUint32(message.Observe)
	if err != nil {
		return false
	}
	if msg.Code() == codes.Content {
		return true
	}
	return false
}

func (b *BlockWise) startSendingMessage(w ResponseWriter, maxSZX SZX, maxMessageSize int, block uint32) error {
	fmt.Printf("%p startSendingMessage: %v\n", b, w.Message())

	payloadSize, err := w.Message().PayloadSize()
	if err != nil {
		return fmt.Errorf("cannot get size of payload: %w", err)
	}

	if payloadSize < int64(maxSZX.Size()) {
		return nil
	}
	sendingMessage := b.acquireMessage(w.Message().Context())
	sendingMessage.SetOptions(w.Message().Options())
	sendingMessage.SetPayload(w.Message().Payload())
	sendingMessage.SetCode(w.Message().Code())
	sendingMessage.SetToken(w.Message().Token())

	_, err = b.handleSendingMessage(w, sendingMessage, maxSZX, maxMessageSize, sendingMessage.Token(), block)
	if err != nil {
		return fmt.Errorf("handleSendingMessage: %w", err)
	}
	if isObserveResponse(w.Message()) {
		// https://tools.ietf.org/html/rfc7959#section-2.6 - we don't need store it because client will be get values via GET.
		return nil
	}
	err = b.sendingMessagesCache.Add(sendingMessage.Token().String(), newRequestGuard(sendingMessage), cache.DefaultExpiration)
	if err != nil {
		return fmt.Errorf("cannot add to response cachce: %w", err)
	}
	return nil
}

func (b *BlockWise) getSendedRequest(token message.Token) Message {
	v, ok := b.bwSendedRequest.Load(token.String())
	if ok {
		return v.(Message)
	}
	globalRequest, ok := b.getSendedRequestFromOutside(token)
	if ok {
		return globalRequest
	}
	return nil
}

func (b *BlockWise) processReceivedMessage(w ResponseWriter, r Message, maxSzx SZX, next func(w ResponseWriter, r Message), blockType message.OptionID, sizeType message.OptionID) error {
	token := r.Token()
	if len(token) == 0 {
		next(w, r)
		return nil
	}
	if r.Code() == codes.GET || r.Code() == codes.DELETE {
		next(w, r)
		return nil
	}

	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		fmt.Printf("%p Handle.processReceivedMessage.GetOptionUint32(blockType) %v %v\n", b, r, err)
		next(w, r)
		return nil
	}
	szx, num, more, err := DecodeBlockOption(block)
	if err != nil {
		return fmt.Errorf("cannot decode block option: %w", err)
	}
	sendedRequest := b.getSendedRequest(token)
	if isObserveResponse(r) {
		// https://tools.ietf.org/html/rfc7959#section-2.6 - performs GET with new token.
		if sendedRequest == nil {
			return fmt.Errorf("observation is not registered")
		}
		token, err = message.GetToken()
		if err != nil {
			return fmt.Errorf("cannot get token for create GET request: %w", err)
		}
		bwSendedRequest := b.acquireMessage(sendedRequest.Context())
		bwSendedRequest.SetCode(sendedRequest.Code())
		bwSendedRequest.SetToken(token)
		bwSendedRequest.SetOptions(sendedRequest.Options())
		b.bwSendedRequest.Store(token.String(), bwSendedRequest)
	}

	fmt.Printf("%p processReceivedMessage: DecodeBlockOption: %v %v %v\n", b, szx, num, more)
	tokenStr := token.String()
	cachedReceivedMessageGuard, ok := b.receivingMessagesCache.Get(tokenStr)
	if !ok {
		if szx > maxSzx {
			szx = maxSzx
		}
		// first request must have 0
		if num != 0 {
			return fmt.Errorf("invalid %v(%v), expected 0", blockType, num)
		}
		// if there is no more then just forward req to next handler
		if more == false {
			next(w, r)
			return nil
		}
		cachedReceivedMessage := b.acquireMessage(r.Context())
		cachedReceivedMessage.SetOptions(r.Options())
		cachedReceivedMessage.SetToken(r.Token())
		cachedReceivedMessageGuard = newRequestGuard(cachedReceivedMessage)
		err := b.receivingMessagesCache.Add(tokenStr, cachedReceivedMessageGuard, cache.DefaultExpiration)
		// request was already stored in cache, silently
		if err != nil {
			return fmt.Errorf("request was already stored in cache")
		}
		cachedReceivedMessage.SetPayload(memfile.New(make([]byte, 0, 1024)))
	}
	messageGuard := cachedReceivedMessageGuard.(*messageGuard)
	defer func(err *error) {
		if *err != nil {
			b.receivingMessagesCache.Delete(tokenStr)
		}
	}(&err)
	messageGuard.Lock()
	defer messageGuard.Unlock()
	cachedReceivedMessage := messageGuard.request
	rETAG, errETAG := r.GetOptionBytes(message.ETag)
	cachedReceivedMessageETAG, errCachedReceivedMessageETAG := cachedReceivedMessage.GetOptionBytes(message.ETag)
	switch {
	case errETAG == nil && errCachedReceivedMessageETAG != nil:
		return fmt.Errorf("received message doesn't contains ETAG but cached received message contains it(%v)", cachedReceivedMessageETAG)
	case errETAG != nil && errCachedReceivedMessageETAG == nil:
		return fmt.Errorf("received message contains ETAG(%v) but cached received message doesn't", rETAG)
	case !bytes.Equal(rETAG, cachedReceivedMessageETAG):
		return fmt.Errorf("received message ETAG(%v) is not equal to cached received message ETAG(%v)", rETAG, cachedReceivedMessageETAG)
	}

	payloadFile := cachedReceivedMessage.Payload().(*memfile.File)
	off := num * szx.Size()
	payloadSize, err := cachedReceivedMessage.PayloadSize()
	if err != nil {
		return fmt.Errorf("cannot get size of payload: %w", err)
	}
	fmt.Printf("%p processReceivedMessage: %p %p, %v payloadSize: %v %v\n", b, messageGuard, cachedReceivedMessage.Payload(), tokenStr, payloadSize, cachedReceivedMessage.Payload())
	if off <= int(payloadSize) {
		copyn, err := payloadFile.Seek(int64(off), io.SeekStart)
		if err != nil {
			return fmt.Errorf("cannot seek to off(%v) of cached request: %w", off, err)
		}
		_, err = r.Payload().Seek(0, io.SeekStart)
		if err != nil {
			return fmt.Errorf("cannot seek to start of request: %w", err)
		}
		written, err := io.Copy(payloadFile, r.Payload())
		if err != nil {
			return fmt.Errorf("cannot copy to cached request: %w", err)
		}
		payloadSize = copyn + written
		fmt.Printf("processReceivedMessage: cachedReceivedMessage.SetPayload: %p %v %v %v %v\n", cachedReceivedMessage.Payload(), copyn, written, payloadSize, cachedReceivedMessage.Payload())
	}
	if !more {
		b.receivingMessagesCache.Delete(tokenStr)
		cachedReceivedMessage.Remove(blockType)
		cachedReceivedMessage.Remove(sizeType)
		cachedReceivedMessage.SetCode(r.Code())
		if !bytes.Equal(cachedReceivedMessage.Token(), token) {
			b.bwSendedRequest.Delete(tokenStr)
		}
		_, err := cachedReceivedMessage.Payload().Seek(0, io.SeekStart)
		if err != nil {
			return fmt.Errorf("cannot seek to start of cachedReceivedMessage request: %w", err)
		}
		next(w, cachedReceivedMessage)
		fmt.Printf("processReceivedMessage: resp %v\n", w.Message())
		return nil
	}
	if szx > maxSzx {
		szx = maxSzx
	}
	num = int(payloadSize) / szx.Size()
	respBlock, err := EncodeBlockOption(szx, num, more)
	if err != nil {
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	sendMessage := b.acquireMessage(r.Context())
	sendMessage.SetToken(token)
	sendMessage.SetCode(codes.Continue)
	if sendedRequest != nil {
		sendMessage.SetOptions(sendedRequest.Options())
		sendMessage.SetCode(sendedRequest.Code())
		sendMessage.Remove(message.Observe)
	}
	sendMessage.SetOptionUint32(blockType, respBlock)
	w.SetMessage(sendMessage)
	return nil
}