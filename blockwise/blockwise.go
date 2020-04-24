package blockwise

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/patrickmn/go-cache"
)

const (
	maxBlockNumber = int(1048575)
	blockWiseDebug = true
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

type ResponseWriter interface {
	Request() Request
	SetRequest(Request)
}

type ReadWriteSeekTruncater interface {
	io.Reader
	io.Writer
	io.Seeker
	Size() int64
	Truncate(n int64) error
}

type Request interface {
	Context() context.Context
	SetCode(codes.Code)
	Code() codes.Code
	SetToken(token []byte)
	Token() []byte
	SetOptionUint32(opt message.OptionID, value uint32)
	GetUint32(id message.OptionID) (uint32, error)
	CopyOptions(interface{})
	Options() interface{}
	Payload() ReadWriteSeekTruncater
	Hijack()
}

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

func EncodeBlockOption(szx BlockWiseSzx, blockNumber int, moreBlocksFollowing bool) (uint32, error) {
	if szx >= BlockWiseSzxCount {
		return 0, ErrInvalidBlockWiseSzx
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

func DecodeBlockOption(blockVal uint32) (szx BlockWiseSzx, blockNumber int, moreBlocksFollowing bool, err error) {
	if blockVal > 0xffffff {
		err = ErrBlockInvalidSize
	}

	szx = BlockWiseSzx(blockVal & 0x7) //masking for the SZX
	if (blockVal & 0x8) != 0 {         //masking for the "M"
		moreBlocksFollowing = true
	}
	blockNumber = int(blockVal) >> 4 //shifting out the SZX and M vals. leaving the block number behind
	if blockNumber > maxBlockNumber {
		err = ErrBlockNumberExceedLimit
	}
	return
}

type BlockWise struct {
	acquireRequest func(ctx context.Context) Request
	releaseRequest func(Request)
	writeRequest   func(Request)
	requestCache   *cache.Cache
	responseCache  *cache.Cache
	errors         func(error)
}

func NewBlockWise(
	acquireRequest func(ctx context.Context) Request,
	releaseRequest func(Request),
	writeRequest func(Request),
	expiration time.Duration,
	errors func(error),
) *BlockWise {
	return &BlockWise{
		acquireRequest: acquireRequest,
		releaseRequest: releaseRequest,
		writeRequest:   writeRequest,
		requestCache:   cache.New(expiration, expiration),
		responseCache:  cache.New(expiration, expiration),
		errors:         errors,
	}
}

func (b *BlockWise) Do(r Request, szx BlockWiseSzx, maxMessageSize int, do func(req Request) (Request, error)) (Request, error) {
	if len(r.Token()) == 0 {
		return nil, fmt.Errorf("invalid token")
	}
	payloadLen := r.Payload().Size()
	if payloadLen <= int64(maxMessageSize) {
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
	req.CopyOptions(r.Options())
	req.SetOptionUint32(sizeID, uint32(payloadLen))

	num := 0
	szx = 0
	for {
		off := int64(num * szxToBytes[szx])
		_, err := r.Payload().Seek(off, 1)
		if err != nil {
			return nil, fmt.Errorf("cannot seek in payload: %w", err)
		}
		err = r.Payload().Truncate(0)
		if err != nil {
			return nil, fmt.Errorf("cannot truncate bw request: %w", err)
		}
		written, err := io.CopyN(req.Payload(), r.Payload(), int64(szxToBytes[szx]))
		if err == io.EOF {
			if off+written == payloadLen {
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("cannot copy payload to bw request: %w", err)
		}
		more := true
		if off+written == payloadLen {
			more = false
		}
		block, err := EncodeBlockOption(szx, num, more)
		if err != nil {
			return nil, fmt.Errorf("cannot encode block option(%v, %v, %v) to bw request: %w", szx, num, more, err)
		}
		req.SetOptionUint32(blockID, block)
		resp, err := do(req)
		if err != nil {
			return nil, fmt.Errorf("cannot do bw request: %w", err)
		}
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
	}
}

func fitSZX(r Request, blockType message.OptionID, maxSZX BlockWiseSzx) BlockWiseSzx {
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

func (b *BlockWise) handleSendingResponse(w ResponseWriter, respToSend Request, maxSZX BlockWiseSzx, token []byte, block2 uint32) error {
	szx, num, _, err := DecodeBlockOption(block2)
	if err != nil {
		return fmt.Errorf("cannot decode block2 option: %w", err)
	}
	off := int64(num * szxToBytes[szx])
	if szx > maxSZX {
		szx = maxSZX
	}
	w.Request().SetCode(respToSend.Code())
	w.Request().CopyOptions(respToSend.Options())
	w.Request().SetToken(token)
	payloadSize := respToSend.Payload().Size()
	offSeek, err := respToSend.Payload().Seek(off, 1)
	if err != nil {
		return fmt.Errorf("cannot seek in response: %w", err)
	}
	if off != offSeek {
		return fmt.Errorf("cannot seek to requested offset: %w", err)
	}
	written, err := io.CopyN(w.Request().Payload(), respToSend.Payload(), int64(szxToBytes[szx]))
	if err == io.EOF {
		err = nil
	}
	more := off+written != respToSend.Payload().Size()
	if err != nil {
		return fmt.Errorf("cannot copy part of response: %w", err)
	}
	respToSend.SetOptionUint32(message.Size2, uint32(payloadSize))
	num = int(off + written/int64(szxToBytes[szx]))
	block2, err = EncodeBlockOption(szx, num, more)
	if err != nil {
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	respToSend.SetOptionUint32(message.Block2, block2)
	return nil
}

func (b *BlockWise) Handle(w ResponseWriter, r Request, maxSZX BlockWiseSzx, next func(w ResponseWriter, r Request)) {
	token := r.Token()
	if len(token) == 0 {
		err := b.handleReceivingRequest(w, r, maxSZX, next)
		if err != nil {
			w.Request().SetCode(codes.Empty)
			b.errors(fmt.Errorf("handleReceivingRequest: %w", err))
		}
		return
	}
	tokenStr := TokenToStr(token)
	v, ok := b.responseCache.Get(tokenStr)
	if !ok {
		err := b.handleReceivingRequest(w, r, maxSZX, next)
		if err != nil {
			w.Request().SetCode(codes.Empty)
			b.errors(fmt.Errorf("handleReceivingRequest: %w", err))
		}
		return
	}
	resp := v.(Request)
	block, err := r.GetUint32(message.Block2)
	if err != nil {
		b.responseCache.Delete(tokenStr)
		b.errors(fmt.Errorf("cannot get block2 option: %w", err))
		return
	}
	err = b.handleSendingResponse(w, resp, maxSZX, token, block)
	if err != nil {
		w.Request().SetCode(codes.Empty)
		b.responseCache.Delete(tokenStr)
		b.errors(fmt.Errorf("handleSendingResponse: %w", err))
		return
	}
}

func (b *BlockWise) handleReceivingRequest(w ResponseWriter, r Request, maxSZX BlockWiseSzx, next func(w ResponseWriter, r Request)) error {
	switch r.Code() {
	case codes.CSM, codes.Ping, codes.Pong, codes.Release, codes.Abort, codes.Empty, codes.Continue:
		next(w, r)
		return nil
	case codes.POST, codes.PUT:
		maxSZX = fitSZX(r, message.Block1, maxSZX)
		err := b.processReceivingRequest(w, r, maxSZX, next, message.Block1)
		if err != nil {
			return err
		}
	default:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		err := b.processReceivingRequest(w, r, maxSZX, next, message.Block2)
		if err != nil {
			return err
		}
	}
	return b.startSendingResponse(w, maxSZX)
}

func (b *BlockWise) startSendingResponse(w ResponseWriter, maxSZX BlockWiseSzx) error {
	if w.Request().Payload().Size() < int64(szxToBytes[maxSZX]) {
		return nil
	}

	respToSend := w.Request()
	respToSend.Hijack()
	w.SetRequest(b.acquireRequest(respToSend.Context()))
	block2, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		w.Request().SetCode(codes.InternalServerError)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	err = b.handleSendingResponse(w, respToSend, maxSZX, respToSend.Token(), block2)
	if err != nil {
		w.Request().SetCode(codes.InternalServerError)
		return fmt.Errorf("handleSendingResponse: %w", err)
	}
	err = b.responseCache.Add(TokenToStr(respToSend.Token()), respToSend, cache.DefaultExpiration)
	if err != nil {
		w.Request().SetCode(codes.InternalServerError)
		return fmt.Errorf("cannot add to response cachce: %w", err)
	}
	return nil
}

func (b *BlockWise) processReceivingRequest(w ResponseWriter, r Request, maxSzx BlockWiseSzx, next func(w ResponseWriter, r Request), blockType message.OptionID) error {
	token := r.Token()
	if len(token) == 0 {
		next(w, r)
		return nil
	}
	tokenStr := TokenToStr(token)
	block, err := r.GetUint32(blockType)
	if err != nil {
		next(w, r)
		return nil
	}
	szx, num, more, err := DecodeBlockOption(block)
	if err != nil {
		return fmt.Errorf("cannot decode block option: %w", err)
	}
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
		r.Hijack()
		b.requestCache.Add(tokenStr, r, cache.DefaultExpiration)
		// request was already stored in cache, silently
		if err != nil {
			return fmt.Errorf("request was already stored in cache")
		}
		num = int(r.Payload().Size()/int64(szxToBytes[szx])) + 1
		respBlock, err := EncodeBlockOption(szx, num, more)
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
		}
		w.Request().SetCode(codes.Continue)
		w.Request().SetOptionUint32(blockType, respBlock)
		return nil
	}
	cachedReq := cachedReqRaw.(Request)
	off := num * szxToBytes[szx]
	payloadSize := cachedReq.Payload().Size()
	if off <= int(payloadSize) {
		offStart, err := cachedReq.Payload().Seek(int64(off), 1)
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot seek in cached request: %w", err)
		}
		written, err := io.Copy(cachedReq.Payload(), r.Payload())
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot copy to cached request: %w", err)
		}
		err = cachedReq.Payload().Truncate(offStart + written)
		if err != nil {
			b.requestCache.Delete(tokenStr)
			return fmt.Errorf("cannot truncate cached request: %w", err)
		}
	}
	if !more {
		b.requestCache.Delete(tokenStr)
		next(w, r)
		return nil
	}
	if szx > maxSzx {
		szx = maxSzx
	}
	num = int(cachedReq.Payload().Size() / int64(szxToBytes[szx]))
	respBlock, err := EncodeBlockOption(szx, num, more)
	if err != nil {
		b.requestCache.Delete(tokenStr)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	w.Request().SetCode(codes.Continue)
	w.Request().SetOptionUint32(blockType, respBlock)
	return nil
}
