package blockwise

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"sync"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/plgd-dev/go-coap/v3/pkg/math"
	"go.uber.org/atomic"
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
	szxMask                                 = 0x7
	DefaultReceivingMessagesCacheMaxEntries = 4096
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
	blockVal := math.CastTo[uint32](blockNumber << 4)
	m := uint32(0)
	if moreBlocksFollowing {
		m = 1
	}
	blockVal += m << 3
	blockVal += math.CastTo[uint32](szx)
	return blockVal, nil
}

// DecodeBlockOption decodes coap block option to block values.
func DecodeBlockOption(blockVal uint32) (szx SZX, blockNumber int64, moreBlocksFollowing bool, err error) {
	if blockVal > maxBlockValue {
		err = ErrBlockInvalidSize
		return
	}

	szx = math.CastTo[SZX](blockVal & szxMask)     // masking for the SZX
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
	cc                               C
	receivingMessagesCache           *cache.Cache[uint64, *messageGuard]
	receivingMessagesCacheMaxEntries *atomic.Int64
	sendingMessagesCache             *cache.Cache[uint64, *sendingMessage]
	errors                           func(error)
	getSentRequestFromOutside        func(token message.Token) (*pool.Message, bool)
	expiration                       time.Duration
	rollingExpiration                bool
	maxReceiveBodySize               int64
}

// SetRollingExpiration resets the Block1 transfer expiration on each new block.
func (b *BlockWise[C]) SetRollingExpiration(enabled bool) {
	b.rollingExpiration = enabled
}

// SetMaxReceiveBodySize limits the Block1 body size, 0 means unlimited.
func (b *BlockWise[C]) SetMaxReceiveBodySize(size int64) {
	b.maxReceiveBodySize = max(size, 0)
}

type sendingMessage struct {
	*pool.Message
	// mutex protects Message's lifetime and the body's seek position.
	mutex sync.Mutex
	// matchingKey allows a Block2 continuation with a changed token to resume the
	// transfer (RFC 9175 §3.3), 0 disables it.
	matchingKey uint64
	requestCode codes.Code
	endpoint    string
}

// sendingMatch describes the request whose response is sent block-wise.
type sendingMatch struct {
	key      uint64
	code     codes.Code
	endpoint string
}

func newSendingMessage(msg *pool.Message) *sendingMessage {
	return &sendingMessage{Message: msg}
}

type messageGuard struct {
	*pool.Message
	*semaphore.Weighted
	// matchingKey (RFC 9175 §3.3) and received are read without the semaphore,
	// so a token-changed continuation never waits for another transfer.
	matchingKey uint64
	received    atomic.Int64
}

var errReceivingMessagesCacheFull = errors.New("receiving messages cache is full")

func newRequestGuard(request *pool.Message, matchingKey uint64) *messageGuard {
	return &messageGuard{
		Message:     request,
		Weighted:    semaphore.NewWeighted(1),
		matchingKey: matchingKey,
	}
}

// New provides blockwise.
// getSentRequestFromOutside must returns a copy of request which will be released after use.
func New[C Client](
	cc C,
	expiration time.Duration,
	errors func(error),
	getSentRequestFromOutside func(token message.Token) (*pool.Message, bool),
) *BlockWise[C] {
	if getSentRequestFromOutside == nil {
		getSentRequestFromOutside = func(message.Token) (*pool.Message, bool) { return nil, false }
	}
	return &BlockWise[C]{
		cc:                               cc,
		receivingMessagesCache:           cache.NewCache[uint64, *messageGuard](),
		receivingMessagesCacheMaxEntries: atomic.NewInt64(DefaultReceivingMessagesCacheMaxEntries),
		sendingMessagesCache:             cache.NewCache[uint64, *sendingMessage](),
		errors:                           errors,
		getSentRequestFromOutside:        getSentRequestFromOutside,
		expiration:                       expiration,
	}
}

// SetReceivingMessagesCacheMaxEntries sets max entries for in-flight incoming
// block-wise assemblies per connection, including concurrent transfers.
// Value 0 means unlimited.
func (b *BlockWise[C]) SetReceivingMessagesCacheMaxEntries(maxEntries int) {
	b.receivingMessagesCacheMaxEntries.Store(int64(max(maxEntries, 0)))
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

func (b *BlockWise[C]) cloneMessage(r *pool.Message) *pool.Message {
	req := b.cc.AcquireMessage(r.Context())
	req.SetCode(r.Code())
	req.SetToken(r.Token())
	req.ResetOptionsTo(r.Options())
	req.SetType(r.Type())
	return req
}

func payloadSizeError(err error) error {
	return fmt.Errorf("cannot get size of payload: %w", err)
}

// Do sends an coap message and returns an coap response via blockwise transfer.
func (b *BlockWise[C]) Do(r *pool.Message, maxSzx SZX, maxMessageSize uint32, remoteAddr net.Addr, do func(req *pool.Message) (*pool.Message, error)) (*pool.Message, error) {
	if maxSzx > SZXBERT {
		return nil, errors.New("invalid szx")
	}
	exchangeKey, err := getExchangeKey(r.Token(), remoteAddr)
	if err != nil {
		return nil, err
	}

	expire, ok := r.Context().Deadline()
	if !ok {
		expire = time.Now().Add(b.expiration)
	}
	sending := newSendingMessage(r)
	sending.mutex.Lock()
	_, loaded := b.sendingMessagesCache.LoadOrStore(exchangeKey, cache.NewElement(sending, expire, nil))
	if loaded {
		sending.mutex.Unlock()
		return nil, errors.New("invalid token")
	}
	defer func() {
		sending.mutex.Lock()
		defer sending.mutex.Unlock()
		b.sendingMessagesCache.Delete(exchangeKey)
		// A continuation may have loaded this entry before it was removed.
		sending.Message = nil
	}()
	req, err := b.prepareSendingRequest(r, maxSzx, maxMessageSize)
	sending.mutex.Unlock()
	if err != nil {
		return nil, err
	}
	if req != r {
		defer b.cc.ReleaseMessage(req)
	}
	return do(req)
}

func getExchangeKey(token message.Token, remoteAddr net.Addr) (uint64, error) {
	if len(token) != 0 {
		return token.Hash(), nil
	}
	if remoteAddr == nil {
		return 0, errors.New("cannot get exchange key: remoteAddr is nil")
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(remoteAddr.String()))
	return h.Sum64(), nil
}

// isRequestCode reports whether code is a request method code (class 0, RFC 7252 §12.1.1).
func isRequestCode(code codes.Code) bool {
	return code >= codes.GET && code < 32
}

func getMessageExchangeKey(r *pool.Message, remoteAddr net.Addr) (uint64, error) {
	if remoteAddr == nil || !isRequestCode(r.Code()) {
		return getExchangeKey(r.Token(), remoteAddr)
	}
	if len(r.Token()) == 0 {
		// Without a token, the request itself identifies the transfer, so tokenless
		// requests for different resources from one endpoint do not share state.
		return getRequestMatchingKey(r, remoteAddr), nil
	}
	if r.Code() != codes.POST && r.Code() != codes.PUT {
		return getExchangeKey(r.Token(), remoteAddr)
	}
	requestKey := getRequestMatchingKey(r, remoteAddr)
	h := fnv.New64a()
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], requestKey)
	_, _ = h.Write(key[:])
	_, _ = h.Write(r.Token())
	return h.Sum64(), nil
}

// getRequestMatchingKey identifies "matchable" requests (RFC 9175 §3.3): same endpoint,
// code and options, ignoring block-wise and elective NoCacheKey options.
func getRequestMatchingKey(r *pool.Message, remoteAddr net.Addr) uint64 {
	h := fnv.New64a()
	writeExchangeKeyPart(h, []byte(remoteAddr.String()))
	var code [2]byte
	binary.BigEndian.PutUint16(code[:], uint16(r.Code()))
	_, _ = h.Write(code[:])
	for _, option := range r.Options() {
		if option.ID == message.Block1 || option.ID == message.Block2 {
			continue
		}
		// RFC 9175 §3.3 matches requests without elective NoCacheKey options (RFC 7252 §5.4.6).
		if option.ID&0x1f == 0x1c {
			continue
		}
		var optionHeader [10]byte
		binary.BigEndian.PutUint16(optionHeader[:2], uint16(option.ID))
		binary.BigEndian.PutUint64(optionHeader[2:], uint64(len(option.Value)))
		_, _ = h.Write(optionHeader[:])
		_, _ = h.Write(option.Value)
	}
	return h.Sum64()
}

func (b *BlockWise[C]) resolveBlock1ExchangeKey(r *pool.Message, remoteAddr net.Addr, exchangeKey uint64) (uint64, error) {
	if (r.Code() != codes.POST && r.Code() != codes.PUT) || !r.HasOption(message.Block1) {
		return exchangeKey, nil
	}
	if b.receivingMessagesCache.Load(exchangeKey) != nil || len(r.Token()) == 0 || remoteAddr == nil {
		return exchangeKey, nil
	}
	block, err := r.GetOptionUint32(message.Block1)
	if err != nil {
		return exchangeKey, err
	}
	szx, num, _, err := DecodeBlockOption(block)
	if err != nil {
		return exchangeKey, err
	}
	if num == 0 {
		return exchangeKey, nil
	}
	expectedOffset := num * szx.Size()
	requestKey := getRequestMatchingKey(r, remoteAddr)
	matchingKey, matches := b.findMatchingBlock1Transfer(requestKey, expectedOffset)
	if matches > 1 {
		return 0, errors.New("cannot identify Block1 continuation: multiple transfers match")
	}
	if matches == 1 {
		return matchingKey, nil
	}
	return exchangeKey, nil
}

// findMatchingBlock1Transfer does not lock the transfers: the received offset is
// published before the 2.31 response is sent, so it is current for the next block.
func (b *BlockWise[C]) findMatchingBlock1Transfer(requestKey uint64, expectedOffset int64) (uint64, int) {
	now := time.Now()
	var matchingKey uint64
	matches := 0
	b.receivingMessagesCache.Range(func(key uint64, element *cache.Element[*messageGuard]) bool {
		if element.IsExpired(now) {
			return true
		}
		guard := element.Data()
		if guard.matchingKey == requestKey && guard.received.Load() == expectedOffset {
			matchingKey = key
			matches++
		}
		return matches < 2
	})
	return matchingKey, matches
}

// isBlock2Continuation reports whether r requests a later block of a representation (RFC 7959 §2.4).
// Block2 num 0 starts a new request, e.g. early block size negotiation.
func isBlock2Continuation(r *pool.Message) bool {
	block, err := r.GetOptionUint32(message.Block2)
	if err != nil {
		return false
	}
	_, num, _, err := DecodeBlockOption(block)
	return err == nil && num > 0
}

// mayResumeBlock2 reports whether r can continue a stored Block2 response.
// An observe registration always starts a new transfer.
func mayResumeBlock2(r *pool.Message) bool {
	switch r.Code() {
	case codes.POST, codes.PUT:
		return isBlock2Continuation(r)
	case codes.GET:
		return !r.HasOption(message.Observe) && isBlock2Continuation(r)
	}
	return false
}

// resolveBlock2ExchangeKey finds the Block2 transfer continued by a request with a
// changed token. A GET continuation is optional state: when it cannot be resolved
// unambiguously, the handler regenerates the representation (RFC 7959 §2.4).
func (b *BlockWise[C]) resolveBlock2ExchangeKey(r *pool.Message, remoteAddr net.Addr, exchangeKey uint64) (uint64, error) {
	if !mayResumeBlock2(r) || b.sendingMessagesCache.Load(exchangeKey) != nil || remoteAddr == nil {
		return exchangeKey, nil
	}
	isGet := r.Code() == codes.GET
	requestKey := getRequestMatchingKey(r, remoteAddr)
	now := time.Now()
	var matchingKey uint64
	matches := 0
	b.sendingMessagesCache.Range(func(key uint64, element *cache.Element[*sendingMessage]) bool {
		if element.IsExpired(now) {
			return true
		}
		sending := element.Data()
		sending.mutex.Lock()
		isMatch := sending.Message != nil && sending.matchingKey == requestKey
		sending.mutex.Unlock()
		if isMatch {
			matchingKey = key
			matches++
		}
		return matches < 2
	})
	if matches > 1 {
		if isGet {
			return exchangeKey, nil
		}
		return 0, errors.New("cannot identify Block2 continuation: multiple transfers match")
	}
	if matches == 1 {
		return matchingKey, nil
	}
	return exchangeKey, nil
}

func writeExchangeKeyPart(h io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

func (b *BlockWise[C]) prepareSendingRequest(r *pool.Message, maxSzx SZX, maxMessageSize uint32) (_ *pool.Message, err error) {
	if r.Body() == nil {
		return r, nil
	}
	payloadSize, err := r.BodySize()
	if err != nil {
		return nil, payloadSizeError(err)
	}
	if payloadSize <= maxSzx.Size() {
		return r, nil
	}

	switch r.Code() {
	case codes.POST, codes.PUT:
		break
	default:
		return nil, fmt.Errorf("unsupported command(%v)", r.Code())
	}
	req := b.cloneMessage(r)
	defer func() {
		if err != nil {
			b.cc.ReleaseMessage(req)
		}
	}()
	payloadSizeUint32, err := math.SafeCastTo[uint32](payloadSize)
	if err != nil {
		return nil, fmt.Errorf("cannot set payload size: %w", err)
	}
	req.SetOptionUint32(message.Size1, payloadSizeUint32)
	block, err := EncodeBlockOption(maxSzx, 0, true)
	if err != nil {
		return nil, fmt.Errorf("cannot encode block option(%v, %v, %v) to bw request: %w", maxSzx, 0, true, err)
	}
	req.SetOptionUint32(message.Block1, block)
	newBufLen := bufferSize(maxSzx, maxMessageSize)
	buf := make([]byte, newBufLen)
	newOff, err := r.Body().Seek(0, io.SeekStart)
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
	return req, nil
}

func newWriteRequestResponse[C Client](cc C, request *pool.Message) *responsewriter.ResponseWriter[C] {
	req := cc.AcquireMessage(request.Context())
	req.SetCode(request.Code())
	req.SetToken(request.Token())
	req.ResetOptionsTo(request.Options())
	req.SetBody(request.Body())
	if request.Type() == message.Confirmable || request.Type() == message.NonConfirmable {
		req.SetType(request.Type())
	}
	return responsewriter.New(req, cc, request.Options()...)
}

// WriteMessage sends an coap message via blockwise transfer.
func (b *BlockWise[C]) WriteMessage(request *pool.Message, maxSZX SZX, maxMessageSize uint32, remoteAddr net.Addr, writeMessage func(r *pool.Message) error) error {
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	exchangeKey, err := getExchangeKey(request.Token(), remoteAddr)
	if err != nil {
		return fmt.Errorf("cannot get exchange key from request(%v): %w", request, err)
	}

	if isObserveResponse(request) && remoteAddr != nil {
		b.invalidateGetTransfers(remoteAddr.String())
	}
	w := newWriteRequestResponse(b.cc, request)
	err = b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock, exchangeKey, sendingMatch{})
	if err != nil {
		return fmt.Errorf("cannot start writing request: %w", err)
	}
	return writeMessage(w.Message())
}

// invalidateGetTransfers is called when a notification announces a new representation
// (RFC 7641 §4.2). Continuations of notifications (RFC 7959 §2.6, §3.4) must not be
// resumed from an older GET response: transfers keyed by the request itself
// (tokenless) are dropped, others are only kept for continuations with their own token.
func (b *BlockWise[C]) invalidateGetTransfers(endpoint string) {
	var drop []uint64
	b.sendingMessagesCache.Range(func(key uint64, element *cache.Element[*sendingMessage]) bool {
		sending := element.Data()
		sending.mutex.Lock()
		if sending.requestCode == codes.GET && sending.endpoint == endpoint && sending.matchingKey != 0 {
			if key == sending.matchingKey {
				drop = append(drop, key)
			}
			sending.matchingKey = 0
		}
		sending.mutex.Unlock()
		return true
	})
	for _, key := range drop {
		b.sendingMessagesCache.Delete(key)
	}
}

func fitSZX(r *pool.Message, blockType message.OptionID, maxSZX SZX) SZX {
	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		return maxSZX
	}

	szx, _, _, err := DecodeBlockOption(block)
	if err != nil {
		return maxSZX
	}

	if maxSZX > szx {
		return szx
	}
	return maxSZX
}

func (b *BlockWise[C]) sendEntityIncomplete(w *responsewriter.ResponseWriter[C], token message.Token) {
	sendMessage := b.cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.RequestEntityIncomplete)
	sendMessage.SetToken(token)
	sendMessage.SetType(message.NonConfirmable)
	w.SetMessage(sendMessage)
}

func (b *BlockWise[C]) sendServiceUnavailable(w *responsewriter.ResponseWriter[C], token message.Token) {
	sendMessage := b.cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.ServiceUnavailable)
	sendMessage.SetToken(token)
	sendMessage.SetType(message.NonConfirmable)
	w.SetMessage(sendMessage)
}

// sendEntityTooLarge responds with 4.13 and the size limit in Size1 (RFC 7959 §4).
func (b *BlockWise[C]) sendEntityTooLarge(w *responsewriter.ResponseWriter[C], token message.Token) {
	sendMessage := b.cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.RequestEntityTooLarge)
	sendMessage.SetToken(token)
	sendMessage.SetType(message.NonConfirmable)
	size1 := min(b.maxReceiveBodySize, int64(^uint32(0)))
	sendMessage.SetOptionUint32(message.Size1, math.CastTo[uint32](size1))
	w.SetMessage(sendMessage)
}

func wantsToBeReceived(r *pool.Message) bool {
	hasBlock1 := r.HasOption(message.Block1)
	hasBlock2 := r.HasOption(message.Block2)
	if hasBlock1 && (r.Code() == codes.POST || r.Code() == codes.PUT) {
		// r contains payload which we received
		return true
	}
	if hasBlock2 && (r.Code() >= codes.GET && r.Code() <= codes.DELETE) {
		// r is command to get next block
		return false
	}
	if r.Code() == codes.Continue {
		return false
	}
	return true
}

func (b *BlockWise[C]) getSendingMessageCode(token uint64) (codes.Code, bool) {
	v := b.sendingMessagesCache.Load(token)
	if v == nil {
		return codes.Empty, false
	}
	sending := v.Data()
	sending.mutex.Lock()
	defer sending.mutex.Unlock()
	if sending.Message == nil {
		return codes.Empty, false
	}
	return sending.Code(), true
}

// Handle middleware which constructs COAP request from blockwise transfer and send COAP response via blockwise.
func (b *BlockWise[C]) Handle(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, remoteAddr net.Addr, next func(w *responsewriter.ResponseWriter[C], r *pool.Message)) {
	if maxSZX > SZXBERT {
		panic("invalid maxSZX")
	}
	token := r.Token()

	exchangeKey, err := getMessageExchangeKey(r, remoteAddr)
	if err != nil {
		b.errors(fmt.Errorf("cannot get exchange key from request(%v): %w", r, err))
		return
	}
	exchangeKey, err = b.resolveBlock1ExchangeKey(r, remoteAddr, exchangeKey)
	if err != nil {
		b.sendEntityIncomplete(w, token)
		b.errors(fmt.Errorf("cannot resolve exchange key for request(%v): %w", r, err))
		return
	}
	exchangeKey, err = b.resolveBlock2ExchangeKey(r, remoteAddr, exchangeKey)
	if err != nil {
		b.sendEntityIncomplete(w, token)
		b.errors(fmt.Errorf("cannot resolve Block2 exchange key for request(%v): %w", r, err))
		return
	}

	sendingMessageCode, sendingMessageExist := b.getSendingMessageCode(exchangeKey)
	if !sendingMessageExist || wantsToBeReceived(r) {
		err = b.handleReceivedMessage(w, r, maxSZX, maxMessageSize, remoteAddr, exchangeKey, next)
		if err != nil {
			if errors.Is(err, errReceivingMessagesCacheFull) {
				b.sendServiceUnavailable(w, token)
			} else {
				b.sendEntityIncomplete(w, token)
			}
			b.errors(fmt.Errorf("handleReceivedMessage(%v): %w", r, err))
		}
		return
	}
	more, err := b.continueSendingMessage(w, r, maxSZX, maxMessageSize, sendingMessageCode, exchangeKey)
	if err != nil {
		b.sendingMessagesCache.Delete(exchangeKey)
		b.errors(fmt.Errorf("continueSendingMessage(%v): %w", r, err))
		return
	}
	// For codes GET,POST,PUT,DELETE, we want them to wait for pairing response and then delete them when the full response comes in or when timeout occurs.
	if !more && sendingMessageCode > codes.DELETE {
		b.sendingMessagesCache.Delete(exchangeKey)
	}
}

func (b *BlockWise[C]) handleReceivedMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, remoteAddr net.Addr, exchangeKey uint64, next func(w *responsewriter.ResponseWriter[C], r *pool.Message)) error {
	startSendingMessageBlock, err := EncodeBlockOption(maxSZX, 0, true)
	if err != nil {
		return fmt.Errorf("cannot encode start sending message block option(%v,%v,%v): %w", maxSZX, 0, true, err)
	}
	switch r.Code() {
	case codes.Empty, codes.CSM, codes.Ping, codes.Pong, codes.Release, codes.Abort:
		next(w, r)
		return nil
	case codes.GET, codes.DELETE:
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		block, errG := r.GetOptionUint32(message.Block2)
		next(w, r)
		if w.Message().Code() == codes.Content && errG == nil {
			startSendingMessageBlock = block
		}
	case codes.POST, codes.PUT:
		maxSZX = fitSZX(r, message.Block1, maxSZX)
		errP := b.processReceivedMessage(w, r, maxSZX, next, transferKeys{exchange: exchangeKey, matching: requestMatchingKey(r, remoteAddr)}, message.Block1, message.Size1)
		if errP != nil {
			return errP
		}
	default:
		if r.Code() >= codes.Created {
			// Block1 in a response only acknowledges the request transfer (RFC 7959 §2.3).
			r.Remove(message.Block1)
		}
		maxSZX = fitSZX(r, message.Block2, maxSZX)
		errP := b.processReceivedMessage(w, r, maxSZX, next, transferKeys{exchange: exchangeKey}, message.Block2, message.Size2)
		if errP != nil {
			return errP
		}
	}
	var match sendingMatch
	if remoteAddr != nil && (r.Code() == codes.GET || r.Code() == codes.POST || r.Code() == codes.PUT) {
		match = sendingMatch{key: getRequestMatchingKey(r, remoteAddr), code: r.Code(), endpoint: remoteAddr.String()}
	}
	if err = b.startSendingMessage(w, maxSZX, maxMessageSize, startSendingMessageBlock, exchangeKey, match); err != nil {
		return err
	}
	echoFinalBlock1(w.Message(), r)
	return nil
}

// echoFinalBlock1 adds the Block1 option of the last request block to the final
// response (RFC 7959 §2.3, §3.2). It is set after startSendingMessage so the
// stored Block2 response body does not carry Block1 in follow-up blocks.
func echoFinalBlock1(resp, r *pool.Message) {
	if r.Code() != codes.POST && r.Code() != codes.PUT {
		return
	}
	block, err := r.GetOptionUint32(message.Block1)
	if err != nil {
		return
	}
	_, _, more, err := DecodeBlockOption(block)
	if err != nil || more {
		return
	}
	switch resp.Code() {
	case codes.Empty, codes.Continue, codes.RequestEntityIncomplete, codes.RequestEntityTooLarge:
		return
	}
	if resp.HasOption(message.Block1) {
		return
	}
	resp.SetOptionUint32(message.Block1, block)
}

func (b *BlockWise[C]) createSendingMessage(sendingMessage *pool.Message, maxSZX SZX, maxMessageSize uint32, block uint32) (sendMessage *pool.Message, more bool, err error) {
	blockType := message.Block2
	sizeType := message.Size2
	token := sendingMessage.Token()
	switch sendingMessage.Code() {
	case codes.POST, codes.PUT:
		blockType = message.Block1
		sizeType = message.Size1
	}

	szx, num, _, err := DecodeBlockOption(block)
	if err != nil {
		return nil, false, fmt.Errorf("cannot decode %v option: %w", blockType, err)
	}

	sendMessage = b.cc.AcquireMessage(sendingMessage.Context())
	sendMessage.SetCode(sendingMessage.Code())
	sendMessage.ResetOptionsTo(sendingMessage.Options())
	sendMessage.SetToken(token)
	sendMessage.SetType(sendingMessage.Type())
	payloadSize, err := sendingMessage.BodySize()
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, payloadSizeError(err)
	}
	szx = getSzx(szx, maxSZX)
	newBufLen := bufferSize(szx, maxMessageSize)
	off := num * szx.Size()
	if blockType == message.Block1 {
		// For block1, we need to skip the already sent bytes.
		off += newBufLen
	}
	offSeek, err := sendingMessage.Body().Seek(off, io.SeekStart)
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, fmt.Errorf("cannot seek in response: %w", err)
	}
	if off != offSeek {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, fmt.Errorf("cannot seek to requested offset(%v != %v)", off, offSeek)
	}
	buf := make([]byte, 1024)
	if int64(len(buf)) < newBufLen {
		buf = make([]byte, newBufLen)
	}
	buf = buf[:newBufLen]

	readed, err := io.ReadFull(sendingMessage.Body(), buf)
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		if offSeek+int64(readed) == payloadSize {
			err = nil
		}
	}
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, fmt.Errorf("cannot read response: %w", err)
	}

	payloadSizeUint32, err := math.SafeCastTo[uint32](payloadSize)
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, fmt.Errorf("cannot set payload size: %w", err)
	}
	sendMessage.SetOptionUint32(sizeType, payloadSizeUint32)

	buf = buf[:readed]
	sendMessage.SetBody(bytes.NewReader(buf))
	more = true
	if offSeek+int64(readed) == payloadSize {
		more = false
	}
	num = offSeek / szx.Size()
	block, err = EncodeBlockOption(szx, num, more)
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return nil, false, fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, more, err)
	}
	sendMessage.SetOptionUint32(blockType, block)
	return sendMessage, more, nil
}

func (b *BlockWise[C]) continueSendingMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSZX SZX, maxMessageSize uint32, sendingMessageCode codes.Code, exchangeKey uint64) (bool, error) {
	blockType := message.Block2
	switch sendingMessageCode {
	case codes.POST, codes.PUT:
		blockType = message.Block1
	}

	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		return false, fmt.Errorf("cannot get %v option: %w", blockType, err)
	}
	value := b.sendingMessagesCache.Load(exchangeKey)
	if value == nil {
		return false, fmt.Errorf("handleSendingMessage: cannot find sending message for token(%v)", r.Token())
	}
	sending := value.Data()
	// Do not hold the cache lock while waiting for or reading a body.
	sending.mutex.Lock()
	defer sending.mutex.Unlock()
	if sending.Message == nil {
		return false, fmt.Errorf("handleSendingMessage: cannot find sending message for token(%v)", r.Token())
	}
	sendMessage, more, err := b.createSendingMessage(sending.Message, maxSZX, maxMessageSize, block)
	if err != nil {
		return false, fmt.Errorf("handleSendingMessage: cannot create sending message: %w", err)
	}
	if isRequestCode(r.Code()) {
		// the continuation may use a new token, the response must echo it (RFC 7252 §5.3.2)
		sendMessage.SetToken(r.Token())
	}
	w.SetMessage(sendMessage)
	return more, err
}

func isObserveResponse(msg *pool.Message) bool {
	_, err := msg.GetOptionUint32(message.Observe)
	if err != nil {
		return false
	}
	return msg.Code() >= codes.Created
}

func (b *BlockWise[C]) startSendingMessage(w *responsewriter.ResponseWriter[C], maxSZX SZX, maxMessageSize uint32, block uint32, exchangeKey uint64, match sendingMatch) error {
	payloadSize, err := w.Message().BodySize()
	if err != nil {
		return payloadSizeError(err)
	}

	if payloadSize < maxSZX.Size() {
		return nil
	}
	sendingMessage, _, err := b.createSendingMessage(w.Message(), maxSZX, maxMessageSize, block)
	if err != nil {
		return fmt.Errorf("handleSendingMessage: cannot create sending message: %w", err)
	}
	originalSendingMessage := w.Swap(sendingMessage)
	if isObserveResponse(w.Message()) {
		b.cc.ReleaseMessage(originalSendingMessage)
		// https://tools.ietf.org/html/rfc7959#section-2.6 - we don't need store it because client will be get values via GET.
		return nil
	}
	expire, ok := sendingMessage.Context().Deadline()
	if !ok {
		expire = time.Now().Add(b.expiration)
	}
	sending := newSendingMessage(originalSendingMessage)
	sending.matchingKey = match.key
	sending.requestCode = match.code
	sending.endpoint = match.endpoint
	el, loaded := b.sendingMessagesCache.LoadOrStore(exchangeKey, cache.NewElement(sending, expire, nil))
	if loaded {
		defer b.cc.ReleaseMessage(originalSendingMessage)
		existing := el.Data()
		existing.mutex.Lock()
		defer existing.mutex.Unlock()
		return fmt.Errorf("cannot add message (%v) to sending message cache: message(%v) with token(%v) already exist", originalSendingMessage, existing.Message, sendingMessage.Token())
	}
	return nil
}

func (b *BlockWise[C]) getSentRequest(exchangeKey uint64, token message.Token) *pool.Message {
	if value := b.sendingMessagesCache.Load(exchangeKey); value != nil {
		sending := value.Data()
		sending.mutex.Lock()
		if sending.Message != nil {
			msg := b.cloneMessage(sending.Message)
			sending.mutex.Unlock()
			return msg
		}
		sending.mutex.Unlock()
	}
	if len(token) == 0 {
		return nil
	}
	globalRequest, ok := b.getSentRequestFromOutside(token)
	if ok {
		return globalRequest
	}
	return nil
}

func (b *BlockWise[C]) handleObserveResponse(sentRequest *pool.Message) (message.Token, time.Time, error) {
	// https://tools.ietf.org/html/rfc7959#section-2.6 - performs GET with new token.
	if sentRequest == nil {
		return nil, time.Time{}, errors.New("observation is not registered")
	}
	token, err := message.GetToken()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("cannot get token for create GET request: %w", err)
	}
	validUntil := time.Now().Add(b.expiration) // context of observation can be expired.
	bwSentRequest := b.cloneMessage(sentRequest)
	bwSentRequest.SetToken(token)
	_, loaded := b.sendingMessagesCache.LoadOrStore(token.Hash(), cache.NewElement(newSendingMessage(bwSentRequest), validUntil, nil))
	if loaded {
		return nil, time.Time{}, errors.New("cannot process message: message with token already exist")
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
		return nil, 0, payloadSizeError(err)
	}
	return payloadFile, payloadSize, nil
}

// receiveBodySize returns the body size after appending r at off, or Size1 if larger.
func receiveBodySize(r *pool.Message, off int64) (int64, error) {
	size := off
	if r.Body() != nil {
		n, err := r.BodySize()
		if err != nil {
			return 0, payloadSizeError(err)
		}
		size += n
	}
	if size1, err := r.GetOptionUint32(message.Size1); err == nil {
		size = max(size, int64(size1))
	}
	return size, nil
}

// rejectTooLarge responds with 4.13 if appending the Block1 request r at off exceeds
// maxReceiveBodySize, and reports whether it did.
func (b *BlockWise[C]) rejectTooLarge(w *responsewriter.ResponseWriter[C], r *pool.Message, blockType message.OptionID, token message.Token, off int64) (bool, error) {
	if blockType != message.Block1 || b.maxReceiveBodySize <= 0 {
		return false, nil
	}
	size, err := receiveBodySize(r, off)
	if err != nil {
		return false, err
	}
	if size <= b.maxReceiveBodySize {
		return false, nil
	}
	b.sendEntityTooLarge(w, token)
	b.errors(fmt.Errorf("handleReceivedMessage(%v): %w: %v bytes", r, ErrRequestEntityTooLarge, size))
	return true, nil
}

// checkFullBlock verifies that a non-final block carries a full block (RFC 7959 §2.2, RFC 8323 §6).
func checkFullBlock(r *pool.Message, szx SZX) error {
	n, err := r.BodySize()
	if err != nil {
		return payloadSizeError(err)
	}
	if n == szx.Size() || (szx == SZXBERT && n > 0 && n%szx.Size() == 0) {
		return nil
	}
	return fmt.Errorf("%w: non-final block has %v bytes, expected %v", ErrRequestEntityIncomplete, n, szx.Size())
}

func copyToPayloadFromOffset(r *pool.Message, payloadFile *memfile.File, offset int64) (int64, error) {
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
	payloadSize := copyn + written
	err = payloadFile.Truncate(payloadSize)
	if err != nil {
		return 0, fmt.Errorf("cannot truncate cached request: %w", err)
	}
	return payloadSize, nil
}

func acquireMessageGuard(mg *messageGuard) error {
	if err := mg.Acquire(mg.Context(), 1); err != nil {
		return fmt.Errorf("processReceivedMessage: cannot lock message: %w", err)
	}
	return nil
}

func cachedReceivedMessageGuardByToken[C Client](b *BlockWise[C], exchangeKey uint64) *messageGuard {
	if e := b.receivingMessagesCache.Load(exchangeKey); e != nil {
		return e.Data()
	}
	return nil
}

func (b *BlockWise[C]) getOrCreateReceivedMessageGuard(r *pool.Message, exchangeKey, matchingKey uint64, validUntil time.Time) (*messageGuard, bool, error) {
	if mg := cachedReceivedMessageGuardByToken(b, exchangeKey); mg != nil {
		return mg, false, nil
	}
	msg := b.cc.AcquireMessage(r.Context())
	msg.ResetOptionsTo(r.Options())
	msg.SetToken(r.Token())
	msg.SetSequence(r.Sequence())
	msg.SetBody(memfile.New(make([]byte, 0, 1024)))
	msg.SetCode(r.Code())
	mg := newRequestGuard(msg, matchingKey)
	// Lock before publishing so later blocks cannot overtake the first block.
	if err := acquireMessageGuard(mg); err != nil {
		b.cc.ReleaseMessage(msg)
		return nil, false, err
	}
	element, loaded, err := b.receivingMessagesCache.LoadOrStoreWithLimit(exchangeKey, cache.NewElement(mg, validUntil, func(d *messageGuard) {
		if d == nil {
			return
		}
		b.sendingMessagesCache.Delete(exchangeKey)
	}), int(b.receivingMessagesCacheMaxEntries.Load()))
	if err != nil || loaded {
		mg.Release(1)
		b.cc.ReleaseMessage(msg)
		if errors.Is(err, cache.ErrCacheFull) {
			return nil, false, errReceivingMessagesCacheFull
		}
		if err != nil {
			return nil, false, err
		}
		return element.Data(), false, nil
	}
	return mg, true, nil
}

func (b *BlockWise[C]) getCachedReceivedMessage(mg *messageGuard, r *pool.Message, block receivedBlock, validUntil time.Time) (*messageGuard, func(), error) {
	locked := false
	if mg == nil {
		var err error
		mg, locked, err = b.getOrCreateReceivedMessageGuard(r, block.exchangeKey, block.matchingKey, validUntil)
		if err != nil {
			return nil, nil, err
		}
	}
	if !locked {
		if err := acquireMessageGuard(mg); err != nil {
			return nil, nil, err
		}
	}
	return mg, func() { mg.Release(1) }, nil
}

type receivedBlock struct {
	exchangeKey uint64
	matchingKey uint64
	token       message.Token
	blockType   message.OptionID
	sizeType    message.OptionID
	szx         SZX
	reqSzx      SZX
	maxSzx      SZX
	num         int64
	more        bool
}

// transferKeys identify a received transfer: exchange is the cache key, matching
// (RFC 9175 §3.3) finds it for a continuation with a changed token, 0 disables it.
type transferKeys struct {
	exchange uint64
	matching uint64
}

func requestMatchingKey(r *pool.Message, remoteAddr net.Addr) uint64 {
	if remoteAddr == nil {
		return 0
	}
	return getRequestMatchingKey(r, remoteAddr)
}

func (b *BlockWise[C]) processReceivedMessage(w *responsewriter.ResponseWriter[C], r *pool.Message, maxSzx SZX, next func(w *responsewriter.ResponseWriter[C], r *pool.Message), keys transferKeys, blockType message.OptionID, sizeType message.OptionID) error {
	token := r.Token()
	if r.Code() == codes.GET || r.Code() == codes.DELETE {
		next(w, r)
		return nil
	}
	block, err := r.GetOptionUint32(blockType)
	if err != nil {
		if errors.Is(err, message.ErrOptionNotFound) {
			next(w, r)
			return nil
		}
		return fmt.Errorf("cannot get Block(optionID=%d) option: %w", blockType, err)
	}
	szx, num, more, err := DecodeBlockOption(block)
	if err != nil {
		return fmt.Errorf("cannot decode block option: %w", err)
	}
	return b.processReceivedBlock(w, r, next, receivedBlock{
		exchangeKey: keys.exchange, matchingKey: keys.matching, token: token, blockType: blockType, sizeType: sizeType,
		szx: szx, reqSzx: szx, maxSzx: maxSzx, num: num, more: more,
	})
}

func (b *BlockWise[C]) processReceivedBlock(w *responsewriter.ResponseWriter[C], r *pool.Message, next func(w *responsewriter.ResponseWriter[C], r *pool.Message), block receivedBlock) error {
	sentRequest := b.getSentRequest(block.exchangeKey, block.token)
	if sentRequest != nil {
		defer b.cc.ReleaseMessage(sentRequest)
	}
	validUntil := b.getValidUntil(sentRequest)
	if block.blockType == message.Block2 && sentRequest == nil {
		return errors.New("cannot request body without paired request")
	}
	if isObserveResponse(r) {
		token, deadline, err := b.handleObserveResponse(sentRequest)
		if err != nil {
			return fmt.Errorf("cannot process message: %w", err)
		}
		block.token, validUntil = token, deadline
		block.exchangeKey = token.Hash()
	}

	var cachedReceivedMessageGuard *messageGuard
	if e := b.receivingMessagesCache.Load(block.exchangeKey); e != nil {
		cachedReceivedMessageGuard = e.Data()
	}
	if cachedReceivedMessageGuard == nil {
		// RFC 7959 §2.9.2: reject non-initial blocks without transfer state.
		if block.blockType == message.Block1 && block.num != 0 {
			return fmt.Errorf("%w: block %v received without transfer context", ErrRequestEntityIncomplete, block.num)
		}
		// if there is no more then just forward req to next handler
		if !block.more {
			if tooLarge, errS := b.rejectTooLarge(w, r, block.blockType, block.token, 0); tooLarge || errS != nil {
				return errS
			}
			next(w, r)
			return nil
		}
		block.szx = getSzx(block.szx, block.maxSzx)
	}
	cachedReceivedMessageGuard, closeCachedReceivedMessage, err := b.getCachedReceivedMessage(cachedReceivedMessageGuard, r, block, validUntil)
	if err != nil {
		return err
	}
	defer closeCachedReceivedMessage()

	return b.processCachedReceivedBlock(w, r, cachedReceivedMessageGuard, next, block, sentRequest)
}

func (b *BlockWise[C]) processCachedReceivedBlock(w *responsewriter.ResponseWriter[C], r *pool.Message, mg *messageGuard, next func(w *responsewriter.ResponseWriter[C], r *pool.Message), block receivedBlock, sentRequest *pool.Message) (err error) {
	defer func() {
		if err != nil {
			b.receivingMessagesCache.Delete(block.exchangeKey)
		}
	}()
	payloadFile, payloadSize, err := b.getPayloadFromCachedReceivedMessage(r, mg.Message)
	if err != nil {
		return fmt.Errorf("cannot get payload: %w", err)
	}
	if block.blockType == message.Block1 && block.more {
		if err = checkFullBlock(r, block.reqSzx); err != nil {
			return err
		}
	}
	off := block.num * block.szx.Size()
	if block.blockType == message.Block1 && (off > payloadSize || (off < payloadSize && !block.more)) {
		return fmt.Errorf("%w: block %v at offset %v, have %v bytes", ErrRequestEntityIncomplete, block.num, off, payloadSize)
	}
	if tooLarge, errS := b.rejectTooLarge(w, r, block.blockType, block.token, off); tooLarge || errS != nil {
		b.receivingMessagesCache.Delete(block.exchangeKey)
		return errS
	}
	return b.appendReceivedBlock(w, r, mg, next, block, sentRequest, payloadFile, payloadSize, off)
}

func (b *BlockWise[C]) appendReceivedBlock(w *responsewriter.ResponseWriter[C], r *pool.Message, mg *messageGuard, next func(w *responsewriter.ResponseWriter[C], r *pool.Message), block receivedBlock, sentRequest *pool.Message, payloadFile *memfile.File, payloadSize, off int64) error {
	if off != payloadSize {
		return b.sendReceivedBlockContinue(w, r, block, sentRequest, payloadSize)
	}
	payloadSize, err := copyToPayloadFromOffset(r, payloadFile, off)
	if err != nil {
		return fmt.Errorf("cannot copy data to payload: %w", err)
	}
	mg.received.Store(payloadSize)
	cachedReceivedMessage := mg.Message
	b.refreshReceiveExpiration(block, payloadSize > off)
	if block.more {
		return b.sendReceivedBlockContinue(w, r, block, sentRequest, payloadSize)
	}
	b.receivingMessagesCache.Delete(block.exchangeKey)
	cachedReceivedMessage.Remove(block.blockType)
	cachedReceivedMessage.Remove(block.sizeType)
	cachedReceivedMessage.SetType(r.Type())
	if !bytes.Equal(cachedReceivedMessage.Token(), block.token) {
		b.sendingMessagesCache.Delete(block.exchangeKey)
	}
	_, err = cachedReceivedMessage.Body().Seek(0, io.SeekStart)
	if err != nil {
		return fmt.Errorf("cannot seek to start of cachedReceivedMessage request: %w", err)
	}
	next(w, cachedReceivedMessage)
	return nil
}

func (b *BlockWise[C]) refreshReceiveExpiration(block receivedBlock, newData bool) {
	if !block.more || block.blockType != message.Block1 || !b.rollingExpiration || !newData {
		return
	}
	if e := b.receivingMessagesCache.Load(block.exchangeKey); e != nil {
		e.ValidUntil.Store(time.Now().Add(b.expiration))
	}
}

func (b *BlockWise[C]) sendReceivedBlockContinue(w *responsewriter.ResponseWriter[C], r *pool.Message, block receivedBlock, sentRequest *pool.Message, payloadSize int64) error {
	szx := getSzx(block.szx, block.maxSzx)
	num := block.num
	sendMessage := b.cc.AcquireMessage(r.Context())
	sendMessage.SetToken(block.token)
	if block.blockType == message.Block2 {
		num = payloadSize / szx.Size()
		sendMessage.ResetOptionsTo(sentRequest.Options())
		sendMessage.SetCode(sentRequest.Code())
		sendMessage.Remove(message.Observe)
		sendMessage.Remove(message.Block1)
		sendMessage.Remove(message.Size1)
	} else {
		sendMessage.SetCode(codes.Continue)
	}
	respBlock, err := EncodeBlockOption(szx, num, block.more)
	if err != nil {
		b.cc.ReleaseMessage(sendMessage)
		return fmt.Errorf("cannot encode block option(%v,%v,%v): %w", szx, num, block.more, err)
	}
	sendMessage.SetOptionUint32(block.blockType, respBlock)
	w.SetMessage(sendMessage)
	return nil
}
