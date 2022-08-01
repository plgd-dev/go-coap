package blockwise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"golang.org/x/sync/semaphore"
)

// Block Option value is represented: https://tools.ietf.org/html/rfc7959#section-2.2
//  0
//  0 1 2 3 4 5 6 7
// +-+-+-+-+-+-+-+-+
// |  NUM  |M| SZX |
// +-+-+-+-+-+-+-+-+
//  0                   1
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |          NUM          |M| SZX |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//  0                   1                   2
//  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
// |                   NUM                 |M| SZX |
// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

const (
	// max block size is 3bytes: https://tools.ietf.org/html/rfc7959#section-2.1
	maxBlockValue = 0xffffff
	// maxBlockNumber is 20bits (NUM)
	maxBlockNumber = 0xffff7
	// moreBlocksFollowingMask is represented by one bit (M)
	moreBlocksFollowingMask = 0x8
	// szxMask last 3bits represents SZX (SZX)
	szxMask = 0x7
)

// SZX enum representation for the size of the block: https://tools.ietf.org/html/rfc7959#section-2.2
type SZX uint8

const (
	// SZX16 block of size 16bytes
	SZX16 SZX = 0
	// SZX32 block of size 32bytes
	SZX32 SZX = 1
	// SZX64 block of size 64bytes
	SZX64 SZX = 2
	// SZX128 block of size 128bytes
	SZX128 SZX = 3
	// SZX256 block of size 256bytes
	SZX256 SZX = 4
	// SZX512 block of size 512bytes
	SZX512 SZX = 5
	// SZX1024 block of size 1024bytes
	SZX1024 SZX = 6
	// SZXBERT block of size n*1024bytes
	SZXBERT SZX = 7
)

var szxToSize = map[SZX]int64{
	SZX16:   16,
	SZX32:   32,
	SZX64:   64,
	SZX128:  128,
	SZX256:  256,
	SZX512:  512,
	SZX1024: 1024,
	SZXBERT: 1024,
}

// Size number of bytes.
func (s SZX) Size() int64 {
	val, ok := szxToSize[s]
	if ok {
		return val
	}
	return -1
}

// EncodeBlockOption encodes block values to coap option.
func EncodeBlockOption(szx SZX, blockNumber int64, moreBlocksFollowing bool) (uint32, error) {
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
func DecodeBlockOption(blockVal uint32) (szx SZX, blockNumber int64, moreBlocksFollowing bool, err error) {
	if blockVal > maxBlockValue {
		err = ErrBlockInvalidSize
		return
	}

	szx = SZX(blockVal & szxMask)                  // masking for the SZX
	if (blockVal & moreBlocksFollowingMask) != 0 { // masking for the "M"
		moreBlocksFollowing = true
	}
	blockNumber = int64(blockVal) >> 4 // shifting out the SZX and M vals. leaving the block number behind
	if blockNumber > maxBlockNumber {
		err = ErrBlockNumberExceedLimit
	}
	return
}

type Client interface {
	// create message from pool
	AcquireMessage(ctx context.Context) *pool.Message
	// return back the message to the pool for next use
	ReleaseMessage(m *pool.Message)
}

type BlockWise[C Client] struct {
	cc                        C
	receivingMessagesCache    *cache.Cache
	sendingMessagesCache      *cache.Cache
	errors                    func(error)
	getSentRequestFromOutside func(token message.Token) (*pool.Message, bool)
	expiration                time.Duration

	bwSentRequest            *senderRequestMap
	autoCleanUpResponseCache bool
}

type messageGuard struct {
	*pool.Message
	*semaphore.Weighted
}

func newRequestGuard(request *pool.Message) *messageGuard {
	return &messageGuard{
		Message:  request,
		Weighted: semaphore.NewWeighted(1),
	}
}

// New provides blockwise.
// getSentRequestFromOutside must returns a copy of request which will be released after use.
func New[C Client](
	cc C,
	expiration time.Duration,
	errors func(error),
	autoCleanUpResponseCache bool,
	getSentRequestFromOutside func(token message.Token) (*pool.Message, bool),
) *BlockWise[C] {
	if getSentRequestFromOutside == nil {
		getSentRequestFromOutside = func(token message.Token) (*pool.Message, bool) { return nil, false }
	}
	return &BlockWise[C]{
		cc:                        cc,
		receivingMessagesCache:    cache.NewCache(),
		sendingMessagesCache:      cache.NewCache(),
		errors:                    errors,
		autoCleanUpResponseCache:  autoCleanUpResponseCache,
		getSentRequestFromOutside: getSentRequestFromOutside,
		bwSentRequest:             newSenderRequestMap(),
		expiration:                expiration,
	}
}

func (b *BlockWise[C]) storeSentRequest(req *senderRequest) error {
	err := b.bwSentRequest.store(req)
	if err != nil {
		return fmt.Errorf("cannot store sent request %v: %w", req.String(), err)
	}
	return nil
}

func bufferSize(szx SZX, maxMessageSize uint32) int64 {
	if szx < SZXBERT {
		return szx.Size()
	}
	return (int64(maxMessageSize) / szx.Size()) * szx.Size()
}

// CheckExpirations iterates over caches and remove expired items.
func (b *BlockWise[C]) CheckExpirations(now time.Time) {
	b.receivingMessagesCache.CheckExpirations(now)
	b.sendingMessagesCache.CheckExpirations(now)
}

// Do sends an coap message and returns an coap response via blockwise transfer.
func (b *BlockWise[C]) Do(r *pool.Message, maxSzx SZX, maxMessageSize uint32, do func(req *pool.Message) (*pool.Message, error)) (*pool.Message, error) {
	if maxSzx > SZXBERT {
		return nil, fmt.Errorf("invalid szx")
	}
	if len(r.Token()) == 0 {
		return nil, fmt.Errorf("invalid token")
	}

	req := b.newSentRequestMessage(r, true)
	defer req.release()
	err := b.storeSentRequest(req)
	if err != nil {
		return nil, err
	}
	defer b.bwSentRequest.deleteByToken(req.Token().Hash())
	if r.Body() == nil {
		return do(r)
	}
	payloadSize, err := r.BodySize()
	if err != nil {
		return nil, fmt.Errorf("cannot get size of payload: %w", err)
	}
	if payloadSize <= maxSzx.Size() {
		return do(r)
	}

	switch r.Code() {
	case codes.POST, codes.PUT:
		break
	default:
		return nil, fmt.Errorf("unsupported command(%v)", r.Code())
	}
	req.SetOptionUint32(message.Size1, uint32(payloadSize))

	num := int64(0)
	buf := make([]byte, 1024)
	szx := maxSzx
	for {
		newBufLen := bufferSize(szx, maxMessageSize)
		if int64(cap(buf)) < newBufLen {
			buf = make([]byte, newBufLen)
		}
		buf = buf[:newBufLen]

		off := num * szx.Size()
		newOff, err := r.Body().Seek(off, io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("cannot seek in payload: %w", err)
		}
		readed, err := io.ReadFull(r.Body(), buf)
		if errors.Is(err, io.ErrUnexpectedEOF) {
			if newOff+int64(readed) == payloadSize {
				err = nil
			}
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read payload: %w", err)
		}
		buf = buf[:readed]
		req.SetBody(bytes.NewReader(buf))
		more := true
		if newOff+int64(readed) == payloadSize {
			more = false
		}
		block, err := EncodeBlockOption(szx, num, more)
		if err != nil {
			return nil, fmt.Errorf("cannot encode block option(%v, %v, %v) to bw request: %w", szx, num, more, err)
		}

		req.SetOptionUint32(message.Block1, block)
		resp, err := do(req.Message)
		if err != nil {
			return nil, fmt.Errorf("cannot do bw request: %w", err)
		}
		block, err = resp.GetOptionUint32(message.Block1)
		if err != nil {
			return resp, nil
		}
		switch resp.Code() {
		case codes.Continue:
		case codes.Created, codes.Changed:
			if !more {
				return resp, nil
			}
		default:
			return resp, nil
		}

		var newSzx SZX
		var newNum int64
		newSzx, newNum, _, err = DecodeBlockOption(block)
		if err != nil {
			return resp, fmt.Errorf("cannot decode block option of bw response: %w", err)
		}
		if num != newNum {
			return resp, fmt.Errorf("unexpected value of acknowledged sequence number(%v != %v)", num, newNum)
		}

		num = num + newSzx.Size()/szx.Size()
		szx = newSzx
	}
}

func newWriteRequestResponse[C Client](cc C, request *pool.Message) *responsewriter.ResponseWriter[C] {
	req := cc.AcquireMessage(request.Context())
	req.SetCode(request.Code())
	req.SetToken(request.Token())
	req.ResetOptionsTo(request.Options())
	req.SetBody(request.Body())
	return responsewriter.New(req, cc, request.Options()...)
}

// WriteMessage sends an coap message via blockwise transfer.
func (b *BlockWise[C]) WriteMessage(request *pool.Message, maxSZX SZX, maxMessageSize uint32, writeMessage func(r *pool.Message) error) error {
	req := b.newSentRequestMessage(request, false)
	err := b.storeSentRequest(req)
	if err != nil {
		return fmt.Errorf("cannot write message: %w", err)
	}
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}

	w := newWriteRequestResponse(b.cc, request)
	err = b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock)
	if err != nil {
		return fmt.Errorf("cannot start writing request: %w", err)
	}
	return writeMessage(w.Message())
}

func fitSZX(r *pool.Message, blockType message.OptionID, maxSZX SZX) SZX {
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

func (b *BlockWise[C]) handleSendingMessage(w *responsewriter.ResponseWriter[C], sendingMessage *pool.Message, maxSZX SZX, maxMessageSize uint32, token []byte, block uint32) (bool, error) {
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
	off := num * szx.Size()
	if szx > maxSZX {
		szx = maxSZX
	}
	sendMessage := b.cc.AcquireMessage(sendingMessage.Context())
	sendMessage.SetCode(sendingMessage.Code())
	sendMessage.ResetOptionsTo(sendingMessage.Options())
	sendMessage.SetToken(token)
	payloadSize, err := sendingMessage.BodySize()
	if err != nil {
		return false, fmt.Errorf("cannot get size of payload: %w", err)
	}
	offSeek, err := sendingMessage.Body().Seek(off, io.SeekStart)
	if err != nil {
		return false, fmt.Errorf("cannot seek in response: %w", err)
	}
	if off != offSeek {
		return false, fmt.Errorf("cannot seek to requested offset(%v != %v)", off, offSeek)
	}
	buf := make([]byte, 1024)
	newBufLen := bufferSize(szx, maxMessageSize)
	if int64(len(buf)) < newBufLen {
		buf = make([]byte, newBufLen)
	}
	buf = buf[:newBufLen]

	readed, err := io.ReadFull(sendingMessage.Body(), buf)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		if offSeek+int64(readed) == payloadSize {
			err = nil
		}
	}
	if err != nil {
		return false, fmt.Errorf("cannot read response: %w", err)
	}

	buf = buf[:readed]
	sendMessage.SetBody(bytes.NewReader(buf))
	more := true
	if offSeek+int64(readed) == payloadSize {
		more = false
	}
	sendMessage.SetOptionUint32(sizeType, uint32(payloadSize))
	num = (offSeek+int64(readed))/szx.Size() - (int64(readed) / szx.Size())
	block, err = EncodeBlockOption(szx, num, more)
	if err != nil {
		return false, fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	sendMessage.SetOptionUint32(blockType, block)
	w.SetMessage(sendMessage)
	return more, nil
}

// RemoveFromResponseCache removes response from cache. It need's tu be used for udp coap.
func (b *BlockWise[C]) RemoveFromResponseCache(token message.Token) {
	if len(token) == 0 {
		return
	}
	b.sendingMessagesCache.Delete(token.Hash())
}

func (b *BlockWise[C]) sendEntityIncomplete(w *responsewriter.ResponseWriter[C], token message.Token) {
	sendMessage := b.cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.RequestEntityIncomplete)
	sendMessage.SetToken(token)
	w.SetMessage(sendMessage)
}

// Handle middleware which constructs COAP request from blockwise transfer and send COAP response via blockwise.
func (b *BlockWise[C]) Handle(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, next func(w *responsewriter.ResponseWriter[C], r *pool.Message)) {
	if maxSZX > SZXBERT {
		panic("invalid maxSZX")
	}
	token := r.Token()

	if len(token) == 0 {
		err := b.handleReceivedMessage(w, r, maxSZX, maxMessageSize, next)
		if err != nil {
			b.sendEntityIncomplete(w, token)
			b.errors(fmt.Errorf("handleReceivedMessage(%v): %w", r, err))
		}
		return
	}
	tokenStr := token.Hash()

	sendingMessageCached := b.sendingMessagesCache.Load(tokenStr)

	if sendingMessageCached == nil {
		err := b.handleReceivedMessage(w, r, maxSZX, maxMessageSize, next)
		if err != nil {
			b.sendEntityIncomplete(w, token)
			b.errors(fmt.Errorf("handleReceivedMessage(%v): %w", r, err))
		}
		return
	}
	more, err := b.continueSendingMessage(w, r, maxSZX, maxMessageSize, sendingMessageCached.Data().(*messageGuard))
	if err != nil {
		b.sendingMessagesCache.Delete(tokenStr)
		b.errors(fmt.Errorf("continueSendingMessage(%v): %w", r, err))
		return
	}
	if b.autoCleanUpResponseCache && !more {
		b.RemoveFromResponseCache(token)
	}
}

func (b *BlockWise[C]) handleReceivedMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, next func(w *responsewriter.ResponseWriter[C], r *pool.Message)) error {
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	switch r.Code() {
	case codes.Empty:
		next(w, r)
		return nil
	case codes.CSM, codes.Ping, codes.Pong, codes.Release, codes.Abort, codes.Continue:
		next(w, r)
		return nil
	case codes.GET, codes.DELETE:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		block, errG := r.GetOptionUint32(message.Block2)
		if errG == nil {
			r.Remove(message.Block2)
		}
		next(w, r)
		if w.Message().Code() == codes.Content && errG == nil {
			startSendingMessageBlock = block
		}
	case codes.POST, codes.PUT:
		maxSZX = fitSZX(r, message.Block1, maxSZX)
		errP := b.processReceivedMessage(w, r, maxSZX, next, message.Block1, message.Size1)
		if errP != nil {
			return errP
		}
	default:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		errP := b.processReceivedMessage(w, r, maxSZX, next, message.Block2, message.Size2)
		if errP != nil {
			return errP
		}
	}
	return b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock)
}

func (b *BlockWise[C]) continueSendingMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, messageGuard *messageGuard) (bool, error) {
	err := messageGuard.Acquire(r.Context(), 1)
	if err != nil {
		return false, fmt.Errorf("cannot lock message: %w", err)
	}
	defer messageGuard.Release(1)
	resp := messageGuard.Message
	blockType := message.Block2
	switch resp.Code() {
	case codes.POST, codes.PUT:
		blockType = message.Block1
	}

	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		return false, fmt.Errorf("cannot get %v option: %w", blockType, err)
	}
	if blockType == message.Block1 {
		// returned blockNumber just acknowledges position we need to set block to the next block.
		szx, _, more, errB := DecodeBlockOption(block)
		if errB != nil {
			return false, fmt.Errorf("cannot decode %v(%v) option: %w", blockType, block, errB)
		}
		off, errB := resp.Body().Seek(0, io.SeekCurrent)
		if errB != nil {
			return false, fmt.Errorf("cannot get current position of seek: %w", errB)
		}
		num := off / szx.Size()
		block, errB = EncodeBlockOption(szx, num, more)
		if errB != nil {
			return false, fmt.Errorf("cannot encode %v(%v, %v, %v) option: %w", blockType, szx, num, more, errB)
		}
	}
	more, err := b.handleSendingMessage(w, resp, maxSZX, maxMessageSize, r.Token(), block)
	if err != nil {
		return false, fmt.Errorf("handleSendingMessage: %w", err)
	}
	return more, err
}

func isObserveResponse(msg *pool.Message) bool {
	_, err := msg.GetOptionUint32(message.Observe)
	if err != nil {
		return false
	}
	if msg.Code() == codes.Content {
		return true
	}
	return false
}

func (b *BlockWise[C]) startSendingMessage(w *responsewriter.ResponseWriter[C], maxSZX SZX, maxMessageSize uint32, block uint32) error {
	payloadSize, err := w.Message().BodySize()
	if err != nil {
		return fmt.Errorf("cannot get size of payload: %w", err)
	}

	if payloadSize < maxSZX.Size() {
		return nil
	}
	sendingMessage := b.cc.AcquireMessage(w.Message().Context())
	sendingMessage.ResetOptionsTo(w.Message().Options())
	sendingMessage.SetBody(w.Message().Body())
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
	expire := time.Now().Add(b.expiration)
	deadline, ok := sendingMessage.Context().Deadline()
	if ok {
		expire = deadline
	}

	el, loaded := b.sendingMessagesCache.LoadOrStore(sendingMessage.Token().Hash(), cache.NewElement(newRequestGuard(sendingMessage), expire, nil))
	if loaded {
		return fmt.Errorf("cannot add message (%v) to sending message cache: message(%v) with token(%v) already exist", sendingMessage, el.Data(), sendingMessage.Token())
	}
	return nil
}

func (b *BlockWise[C]) getSentRequest(token message.Token) *pool.Message {
	var req *pool.Message
	_, ok := b.bwSentRequest.byToken.LoadWithFunc(token.Hash(), func(v *senderRequest) *senderRequest {
		req = b.cc.AcquireMessage(v.Context())
		req.SetCode(v.Code())
		req.SetToken(v.Token())
		req.ResetOptionsTo(v.Options())
		return v
	})
	if ok {
		return req
	}
	globalRequest, ok := b.getSentRequestFromOutside(token)
	if ok {
		return globalRequest
	}
	return nil
}

func (b *BlockWise[C]) handleObserveResponse(r, sentRequest *pool.Message) (message.Token, time.Time, error) {
	// https://tools.ietf.org/html/rfc7959#section-2.6 - performs GET with new token.
	if sentRequest == nil {
		return nil, time.Time{}, fmt.Errorf("observation is not registered")
	}
	token, err := message.GetToken()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cannot get token for create GET request: %w", err)
	}
	validUntil := time.Now().Add(b.expiration) // context of observation can be expired.
	bwSentRequest := b.newSentRequestMessage(sentRequest, true)
	bwSentRequest.SetToken(token)
	if errS := b.storeSentRequest(bwSentRequest); errS != nil {
		return nil, time.Time{}, fmt.Errorf("cannot process message: %w", errS)
	}
	return token, validUntil, nil
}

func (b *BlockWise[C]) getValidUntil(sentRequest *pool.Message) time.Time {
	validUntil := time.Now().Add(b.expiration)
	if sentRequest != nil {
		if deadline, ok := sentRequest.Context().Deadline(); ok {
			return deadline
		}
	}
	return validUntil
}

func getSzx(szx, maxSzx SZX) SZX {
	if szx > maxSzx {
		return maxSzx
	}
	return szx
}

func (b *BlockWise[C]) getPayloadFromCachedReceivedMessage(r, cachedReceivedMessage *pool.Message) (*memfile.File, int64, error) {
	payloadFile, ok := cachedReceivedMessage.Body().(*memfile.File)
	if !ok {
		return nil, 0, fmt.Errorf("invalid body type(%T) stored in receivingMessagesCache", cachedReceivedMessage.Body())
	}
	rETAG, errETAG := r.GetOptionBytes(message.ETag)
	cachedReceivedMessageETAG, errCachedReceivedMessageETAG := cachedReceivedMessage.GetOptionBytes(message.ETag)
	switch {
	case errETAG == nil && errCachedReceivedMessageETAG != nil:
		if len(cachedReceivedMessageETAG) > 0 { // make sure there is an etag there
			return nil, 0, fmt.Errorf("received message doesn't contains ETAG but cached received message contains it(%v)", cachedReceivedMessageETAG)
		}
	case errETAG != nil && errCachedReceivedMessageETAG == nil:
		if len(rETAG) > 0 { // make sure there is an etag there
			return nil, 0, fmt.Errorf("received message contains ETAG(%v) but cached received message doesn't", rETAG)
		}
	case !bytes.Equal(rETAG, cachedReceivedMessageETAG):
		// ETAG was changed - drop data and set new ETAG
		cachedReceivedMessage.SetOptionBytes(message.ETag, rETAG)
		if err := payloadFile.Truncate(0); err != nil {
			return nil, 0, fmt.Errorf("cannot truncate cached request: %w", err)
		}
	}

	payloadSize, err := cachedReceivedMessage.BodySize()
	if err != nil {
		return nil, 0, fmt.Errorf("cannot get size of payload: %w", err)
	}
	return payloadFile, payloadSize, nil
}

func copyToPayloadFromOffset(r *pool.Message, payloadFile *memfile.File, offset int64) (int64, error) {
	payloadSize := int64(0)
	copyn, err := payloadFile.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("cannot seek to off(%v) of cached request: %w", offset, err)
	}
	written := int64(0)
	if r.Body() != nil {
		_, err = r.Body().Seek(0, io.SeekStart)
		if err != nil {
			return 0, fmt.Errorf("cannot seek to start of request: %w", err)
		}
		written, err = io.Copy(payloadFile, r.Body())
		if err != nil {
			return 0, fmt.Errorf("cannot copy to cached request: %w", err)
		}
	}
	payloadSize = copyn + written
	err = payloadFile.Truncate(payloadSize)
	if err != nil {
		return 0, fmt.Errorf("cannot truncate cached request: %w", err)
	}
	return payloadSize, nil
}

func (b *BlockWise[C]) processReceivedMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSzx SZX, next func(w *responsewriter.ResponseWriter[C], r *pool.Message), blockType message.OptionID, sizeType message.OptionID) error {
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
		next(w, r)
		return nil
	}
	szx, num, more, err := DecodeBlockOption(block)
	if err != nil {
		return fmt.Errorf("cannot decode block option: %w", err)
	}
	sentRequest := b.getSentRequest(token)
	if sentRequest != nil {
		defer b.cc.ReleaseMessage(sentRequest)
	}
	validUntil := b.getValidUntil(sentRequest)
	if blockType == message.Block2 && sentRequest == nil {
		return fmt.Errorf("cannot request body without paired request")
	}
	if isObserveResponse(r) {
		token, validUntil, err = b.handleObserveResponse(r, sentRequest)
		if err != nil {
			return fmt.Errorf("cannot process message: %w", err)
		}
	}

	tokenStr := token.Hash()
	var cachedReceivedMessageGuard interface{}
	e := b.receivingMessagesCache.Load(tokenStr)
	if e != nil {
		cachedReceivedMessageGuard = e.Data()
	}
	cannotLockError := func(err error) error {
		return fmt.Errorf("processReceivedMessage: cannot lock message: %w", err)
	}
	var msgGuard *messageGuard
	if cachedReceivedMessageGuard == nil {
		szx = getSzx(szx, maxSzx)
		// if there is no more then just forward req to next handler
		if !more {
			next(w, r)
			return nil
		}
		cachedReceivedMessage := b.cc.AcquireMessage(r.Context())
		cachedReceivedMessage.ResetOptionsTo(r.Options())
		cachedReceivedMessage.SetToken(r.Token())
		cachedReceivedMessage.SetSequence(r.Sequence())
		cachedReceivedMessage.SetBody(memfile.New(make([]byte, 0, 1024)))
		cachedReceivedMessage.SetCode(r.Code())
		msgGuard = newRequestGuard(cachedReceivedMessage)
		errA := msgGuard.Acquire(cachedReceivedMessage.Context(), 1)
		if errA != nil {
			return cannotLockError(errA)
		}
		defer msgGuard.Release(1)
		element, loaded := b.receivingMessagesCache.LoadOrStore(tokenStr, cache.NewElement(msgGuard, validUntil, func(d interface{}) {
			if d == nil {
				return
			}
			b.bwSentRequest.deleteByToken(tokenStr)
		}))
		// request was already stored in cache, silently
		if loaded {
			cachedReceivedMessageGuard = element.Data()
			if cachedReceivedMessageGuard != nil {
				msgGuard = cachedReceivedMessageGuard.(*messageGuard)
				errA := msgGuard.Acquire(cachedReceivedMessage.Context(), 1)
				if errA != nil {
					return cannotLockError(errA)
				}
				defer msgGuard.Release(1)
			} else {
				return fmt.Errorf("request was already stored in cache")
			}
		}
	} else {
		msgGuard = cachedReceivedMessageGuard.(*messageGuard)
		errA := msgGuard.Acquire(msgGuard.Context(), 1)
		if errA != nil {
			return cannotLockError(errA)
		}
		defer msgGuard.Release(1)
	}
	defer func(err *error) {
		if *err != nil {
			b.receivingMessagesCache.Delete(tokenStr)
		}
	}(&err)
	cachedReceivedMessage := msgGuard.Message
	payloadFile, payloadSize, err := b.getPayloadFromCachedReceivedMessage(r, cachedReceivedMessage)
	if err != nil {
		return fmt.Errorf("cannot get payload: %w", err)
	}
	off := num * szx.Size()
	if off == payloadSize {
		payloadSize, err = copyToPayloadFromOffset(r, payloadFile, off)
		if err != nil {
			return fmt.Errorf("cannot copy data to payload: %w", err)
		}
		if !more {
			b.receivingMessagesCache.Delete(tokenStr)
			cachedReceivedMessage.Remove(blockType)
			cachedReceivedMessage.Remove(sizeType)
			setTypeFrom(cachedReceivedMessage, r)
			if !bytes.Equal(cachedReceivedMessage.Token(), token) {
				b.bwSentRequest.deleteByToken(tokenStr)
			}
			_, errS := cachedReceivedMessage.Body().Seek(0, io.SeekStart)
			if errS != nil {
				return fmt.Errorf("cannot seek to start of cachedReceivedMessage request: %w", errS)
			}
			next(w, cachedReceivedMessage)
			return nil
		}
	}

	szx = getSzx(szx, maxSzx)
	sendMessage := b.cc.AcquireMessage(r.Context())
	sendMessage.SetToken(token)
	if blockType == message.Block2 {
		num = payloadSize / szx.Size()
		sendMessage.ResetOptionsTo(sentRequest.Options())
		sendMessage.SetCode(sentRequest.Code())
		sendMessage.Remove(message.Observe)
		sendMessage.Remove(message.Block1)
		sendMessage.Remove(message.Size1)
	} else {
		sendMessage.SetCode(codes.Continue)
	}
	respBlock, err := EncodeBlockOption(szx, num, more)
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	sendMessage.SetOptionUint32(blockType, respBlock)
	w.SetMessage(sendMessage)
	return nil
}
