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

// Message defines message interface for blockwise transfer.
type Message interface {
	Context() context.Context
	SetCode(codes.Code)
	Code() codes.Code
	SetToken(token []byte)
	Token() []byte
	SetUint32(id message.OptionID, value uint32)
	GetUint32(id message.OptionID) (uint32, error)
	Remove(id message.OptionID)
	SetOptions(message.Options)
	Options() message.Options
	SetPayload(r io.ReadSeeker)
	Payload() io.ReadSeeker
	PayloadSize() (int64, error)
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
	acquireRequest           func(ctx context.Context) Message
	releaseRequest           func(Message)
	requestCache             *cache.Cache
	responseCache            *cache.Cache
	errors                   func(error)
	autoCleanUpResponseCache bool
}

type requestGuard struct {
	sync.Mutex
	request Message
}

func newRequestGuard(request Message) *requestGuard {
	return &requestGuard{
		request: request,
	}
}

func NewBlockWise(
	acquireRequest func(ctx context.Context) Message,
	releaseRequest func(Message),
	expiration time.Duration,
	errors func(error),
	autoCleanUpResponseCache bool,
) *BlockWise {
	return &BlockWise{
		acquireRequest:           acquireRequest,
		releaseRequest:           releaseRequest,
		requestCache:             cache.New(expiration, expiration),
		responseCache:            cache.New(expiration, expiration),
		errors:                   errors,
		autoCleanUpResponseCache: autoCleanUpResponseCache,
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
	if r.Payload() == nil {
		return do(r)
	}
	payloadSize, err := r.PayloadSize()
	if err != nil {
		return nil, fmt.Errorf("cannot get size of payload: %w", err)
	}

	if payloadSize <= int64(maxSzx.Size()) {
		return do(r)
	}

	blockID := message.Block2
	sizeID := message.Size2
	switch r.Code() {
	case codes.POST, codes.PUT:
		blockID = message.Block1
		sizeID = message.Size1
	}

	req := b.acquireRequest(r.Context())
	defer b.releaseRequest(req)
	req.SetCode(r.Code())
	req.SetToken(r.Token())
	req.SetOptions(r.Options())
	req.SetUint32(sizeID, uint32(payloadSize))

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

		req.SetUint32(blockID, block)
		resp, err := do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot do bw request: %w", err)
		}
		fmt.Printf("Do: resp %v\n", resp)
		if resp.Code() != codes.Continue {
			return resp, nil
		}
		block, err = resp.GetUint32(blockID)
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
	request Message
}

func (w *writeRequestResponse) SetMessage(r Message) {
	w.request = r
}

func (w *writeRequestResponse) Message() Message {
	return w.request
}

// WriteRequest sends an coap request via blockwise transfer.
func (b *BlockWise) WriteRequest(request Message, maxSzx SZX, maxMessageSize int, writeRequest func(r Message) error) error {
	w := writeRequestResponse{
		request: request,
	}
	err := b.startSendingResponse(&w, maxSzx, maxMessageSize)
	if err != nil {
		return fmt.Errorf("cannot start writing request: %w", err)
	}
	return writeRequest(w.Message())
}

func fitSZX(r Message, blockType message.OptionID, maxSZX SZX) SZX {
	block, err := r.GetUint32(blockType)
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

func (b *BlockWise) handleSendingResponse(w ResponseWriter, respToSend Message, maxSZX SZX, maxMessageSize int, token []byte, block uint32) (bool, error) {
	blockType := message.Block2
	sizeType := message.Size2
	switch respToSend.Code() {
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
	w.Message().SetCode(respToSend.Code())
	w.Message().SetOptions(respToSend.Options())
	w.Message().SetToken(token)
	payloadSize, err := respToSend.PayloadSize()
	if err != nil {
		return false, fmt.Errorf("cannot get size of payload: %w", err)
	}
	offSeek, err := respToSend.Payload().Seek(off, io.SeekStart)
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

	readed, err := io.ReadFull(respToSend.Payload(), buf)
	if err == io.ErrUnexpectedEOF {
		if offSeek+int64(readed) == payloadSize {
			err = nil
		}
	}

	buf = buf[:readed]
	w.Message().SetPayload(bytes.NewReader(buf))
	more := true
	if offSeek+int64(readed) == payloadSize {
		more = false
	}
	w.Message().SetUint32(sizeType, uint32(payloadSize))
	num = (int(offSeek)+readed)/szx.Size() - (readed / szx.Size())
	block, err = EncodeBlockOption(szx, num, more)
	if err != nil {
		return false, fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	w.Message().SetUint32(blockType, block)
	fmt.Printf("%p handleSendingResponse (%v, %v, %v)\n", b, szx, num, more)
	return more, nil
}

// RemoveFromResponseCache removes response from cache. It need's tu be used for udp coap.
func (b *BlockWise) RemoveFromResponseCache(token []byte) {
	if len(token) == 0 {
		return
	}
	b.responseCache.Delete(TokenToStr(token))
}

// Handle middleware which constructs COAP request from blockwise transfer and send COAP response via blockwise.
func (b *BlockWise) Handle(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, next func(w ResponseWriter, r Message)) {
	if maxSZX > SZXBERT {
		panic("invalid maxSZX")
	}
	token := r.Token()
	fmt.Printf("%p Handle %v\n", b, r)
	if len(token) == 0 {
		err := b.handleReceivingRequest(w, r, maxSZX, maxMessageSize, next)
		if err != nil {
			w.Message().SetCode(codes.Empty)
			b.errors(fmt.Errorf("handleReceivingRequest: %w", err))
		}
		return
	}
	tokenStr := TokenToStr(token)
	v, ok := b.responseCache.Get(tokenStr)
	fmt.Printf("%p Handle.responseCache %v %v\n", b, v, ok)
	if !ok {
		err := b.handleReceivingRequest(w, r, maxSZX, maxMessageSize, next)
		fmt.Printf("%p Handle.handleReceivingRequest %v\n", b, err)
		if err != nil {
			w.Message().SetCode(codes.Empty)
			b.errors(fmt.Errorf("handleReceivingRequest: %w", err))
		}
		return
	}
	more, err := b.continueSendingResponse(w, r, maxSZX, maxMessageSize, v.(*requestGuard))
	if err != nil {
		b.responseCache.Delete(tokenStr)
		b.errors(fmt.Errorf("continueSendingResponse: %w", err))
		return
	}
	if b.autoCleanUpResponseCache && more == false {
		b.RemoveFromResponseCache(token)
	}
}

func (b *BlockWise) handleReceivingRequest(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, next func(w ResponseWriter, r Message)) error {
	switch r.Code() {
	case codes.CSM, codes.Ping, codes.Pong, codes.Release, codes.Abort, codes.Empty, codes.Continue:
		next(w, r)
		return nil
	case codes.POST, codes.PUT:
		maxSZX = fitSZX(r, message.Block1, maxSZX)
		err := b.processReceivingRequest(w, r, maxSZX, next, message.Block1, message.Size1)
		if err != nil {
			return err
		}
	default:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		err := b.processReceivingRequest(w, r, maxSZX, next, message.Block2, message.Size2)
		if err != nil {
			return err
		}
	}
	return b.startSendingResponse(w, maxSZX, maxMessageSize)
}

func (b *BlockWise) continueSendingResponse(w ResponseWriter, r Message, maxSZX SZX, maxMessageSize int, reqGuard *requestGuard) (bool, error) {
	reqGuard.Lock()
	defer reqGuard.Unlock()
	resp := reqGuard.request
	blockType := message.Block2
	switch resp.Code() {
	case codes.POST, codes.PUT:
		blockType = message.Block1
	}

	block, err := r.GetUint32(blockType)
	if err != nil {
		return false, fmt.Errorf("cannot get block2 option: %w", err)
	}
	more, err := b.handleSendingResponse(w, resp, maxSZX, maxMessageSize, r.Token(), block)
	fmt.Printf("%p Handle.handleSendingResponse %v\n", b, err)
	if err != nil {
		w.Message().SetCode(codes.Empty)
		return false, fmt.Errorf("handleSendingResponse: %w", err)
	}
	return more, err
}

func (b *BlockWise) startSendingResponse(w ResponseWriter, maxSZX SZX, maxMessageSize int) error {
	fmt.Printf("%p startSendingResponse: %v\n", b, w.Message())

	payloadSize, err := w.Message().PayloadSize()
	if err != nil {
		return fmt.Errorf("cannot get size of payload: %w", err)
	}

	if payloadSize < int64(maxSZX.Size()) {
		return nil
	}
	respToSend := b.acquireRequest(w.Message().Context())
	respToSend.SetOptions(w.Message().Options())
	respToSend.SetPayload(w.Message().Payload())
	respToSend.SetCode(w.Message().Code())
	respToSend.SetToken(w.Message().Token())

	block, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		w.Message().SetCode(codes.InternalServerError)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	_, err = b.handleSendingResponse(w, respToSend, maxSZX, maxMessageSize, respToSend.Token(), block)
	if err != nil {
		w.Message().SetCode(codes.InternalServerError)
		return fmt.Errorf("handleSendingResponse: %w", err)
	}
	err = b.responseCache.Add(TokenToStr(respToSend.Token()), newRequestGuard(respToSend), cache.DefaultExpiration)
	if err != nil {
		w.Message().SetCode(codes.InternalServerError)
		return fmt.Errorf("cannot add to response cachce: %w", err)
	}
	return nil
}

func (b *BlockWise) processReceivingRequest(w ResponseWriter, r Message, maxSzx SZX, next func(w ResponseWriter, r Message), blockType message.OptionID, sizeType message.OptionID) error {
	token := r.Token()
	if len(token) == 0 {
		next(w, r)
		return nil
	}
	tokenStr := TokenToStr(token)
	block, err := r.GetUint32(blockType)
	if err != nil {
		fmt.Printf("%p Handle.processReceivingRequest.GetUint32(blockType) %v %v\n", b, r, err)
		next(w, r)
		return nil
	}
	szx, num, more, err := DecodeBlockOption(block)
	if err != nil {
		return fmt.Errorf("cannot decode block option: %w", err)
	}
	fmt.Printf("%p processReceivingRequest: DecodeBlockOption: %v %v %v\n", b, szx, num, more)
	cachedReqRaw, ok := b.requestCache.Get(tokenStr)
	if !ok {
		if szx > maxSzx {
			szx = maxSzx
		}
		// first request must have 0
		if num != 0 {
			return fmt.Errorf("blockNumber(%v) != 0 : %w", num, err)
		}
		// if there is no more then just forward req to next handler
		if more == false {
			next(w, r)
			return nil
		}
		cachedReq := b.acquireRequest(r.Context())
		cachedReq.SetOptions(r.Options())
		cachedReq.SetCode(r.Code())
		cachedReq.SetToken(r.Token())
		cachedReqRaw = newRequestGuard(cachedReq)
		err := b.requestCache.Add(tokenStr, cachedReqRaw, cache.DefaultExpiration)
		// request was already stored in cache, silently
		if err != nil {
			return fmt.Errorf("request was already stored in cache")
		}
		cachedReq.SetPayload(memfile.New(make([]byte, 0, 1024)))
	}
	reqGuard := cachedReqRaw.(*requestGuard)
	reqGuard.Lock()
	defer reqGuard.Unlock()
	cachedReq := reqGuard.request

	payloadFile := cachedReq.Payload().(*memfile.File)
	off := num * szx.Size()
	payloadSize, err := cachedReq.PayloadSize()
	if err != nil {
		return fmt.Errorf("cannot get size of payload: %w", err)
	}
	fmt.Printf("%p processReceivingRequest: %p %p, %v payloadSize: %v %v\n", b, reqGuard, cachedReq.Payload(), tokenStr, payloadSize, cachedReq.Payload())
	if off <= int(payloadSize) {
		copyn, err := payloadFile.Seek(int64(off), io.SeekStart)
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot seek to off(%v) of cached request: %w", off, err)
		}
		_, err = r.Payload().Seek(0, io.SeekStart)
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot seek to start of request: %w", err)
		}
		written, err := io.Copy(payloadFile, r.Payload())
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot copy to cached request: %w", err)
		}
		payloadSize = copyn + written
		fmt.Printf("processReceivingRequest: cachedReq.SetPayload: %p %v %v %v %v\n", cachedReq.Payload(), copyn, written, payloadSize, cachedReq.Payload())
	}
	if !more {
		b.requestCache.Delete(tokenStr)
		cachedReq.Remove(blockType)
		cachedReq.Remove(sizeType)
		_, err := cachedReq.Payload().Seek(0, io.SeekStart)
		if err != nil {
			return fmt.Errorf("cannot seek to start of cachedReq request: %w", err)
		}
		next(w, cachedReq)
		fmt.Printf("processReceivingRequest: resp %v\n", w.Message())
		return nil
	}
	if szx > maxSzx {
		szx = maxSzx
	}
	num = int(payloadSize) / szx.Size()
	respBlock, err := EncodeBlockOption(szx, num, more)
	if err != nil {
		b.requestCache.Delete(tokenStr)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	w.Message().SetToken(r.Token())
	w.Message().SetCode(codes.Continue)
	w.Message().SetUint32(blockType, respBlock)

	return nil
}
