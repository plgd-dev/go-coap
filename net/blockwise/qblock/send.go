package qblock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

type q2SendTransfer struct {
	message                *pool.Message
	expires                time.Time
	size                   int64
	sentThrough            int64
	delay                  time.Duration
	identity               string
	mode                   message.Type
	terminalObservationKey string
	recoveryUntil          time.Time
}

func q2TransferKey(r *pool.Message, remoteAddr net.Addr) string {
	key := bytes.NewBufferString(qTransferKey(r, remoteAddr, nil, true))
	blocks.WriteExchangeKeyPart(key, r.Token())
	return key.String()
}

func (b *Engine[C]) handleQBlock2(
	w *responsewriter.ResponseWriter[C],
	r *pool.Message,
	maxSZX SZX,
	remoteAddr net.Addr,
	next func(w *responsewriter.ResponseWriter[C], r *pool.Message),
) error {
	szx, num, more, err := b.parseQBlock2(r)
	if err != nil {
		return err
	}
	if szx > maxSZX {
		if num != 0 || optionCount(r, message.QBlock2) != 1 {
			return ErrQBlockTooLarge
		}
		request := b.cloneMessage(r)
		defer b.cc.ReleaseMessage(request)
		request.SetBody(r.Body())
		value, _ := EncodeQBlockOption(maxSZX, 0, more)
		request.SetOptionUint32(message.QBlock2, value)
		return b.handleQBlock2(w, request, maxSZX, remoteAddr, next)
	}
	b.prepareQ2ObservationRequest(r, remoteAddr, num)
	key, err := b.q2ResponseKey(r, remoteAddr, num, more)
	if err != nil {
		return err
	}
	response, err := b.loadQ2Response(w, r, remoteAddr, key, szx, num, next)
	if err != nil {
		return err
	}
	if response.Code() < codes.Created {
		return nil
	}
	size, _ := response.GetOptionUint32(message.Size2)
	count := max(int64(1), (int64(size)+szx.Size()-1)/szx.Size())
	nums, wholeBody, err := b.q2RequestedBlocks(r, szx, count)
	if err != nil || !validQ2First(nums, num) {
		b.cc.ReleaseMessage(response)
		return ErrInvalidOptionBlock2
	}
	if r.Type() == message.NonConfirmable {
		handled, prepareErr := b.prepareQ2NonSend(r, key, szx, nums, count, wholeBody)
		if handled || prepareErr != nil {
			b.cc.ReleaseMessage(response)
			return prepareErr
		}
	}
	if !wholeBody {
		response.Remove(message.Observe)
	}
	w.SetMessage(response)
	return b.continueQ2Response(r, key, szx, nums, count, wholeBody)
}

func (b *Engine[C]) prepareQ2ObservationRequest(r *pool.Message, remoteAddr net.Addr, num int64) {
	if !r.HasOption(message.Observe) {
		return
	}
	observe, err := r.Observe()
	if err != nil {
		return
	}
	b.CancelQBlockObservation(r.Token(), remoteAddr)
	if observe == 0 && num == 0 {
		b.invalidateTerminalQ2Observation(r, remoteAddr)
	}
}

func (b *Engine[C]) invalidateTerminalQ2Observation(r *pool.Message, remoteAddr net.Addr) {
	key := q2TransferKey(r, remoteAddr)
	registrationKey := qObservationKey(r.Token(), remoteAddr)
	b.q2SendMu.Lock()
	transfer := b.q2Senders[key]
	if transfer == nil || transfer.terminalObservationKey != registrationKey {
		b.q2SendMu.Unlock()
		return
	}
	delete(b.q2Senders, key)
	b.q2SendBytes -= transfer.size
	b.qReleaseBytes(transfer.size)
	b.qReleaseTransfer()
	b.cc.ReleaseMessage(transfer.message)
	b.q2SendMu.Unlock()
	if transport, ok := any(b.cc).(Transport); ok {
		transport.CancelQBlock("q2:" + key)
	}
}

func validQ2First(nums []int64, first int64) bool {
	return len(nums) > 0 && nums[0] == first
}

func (b *Engine[C]) parseQBlock2(r *pool.Message) (SZX, int64, bool, error) {
	if r.Code() != codes.GET && !qBodyMethod(r.Code()) {
		return 0, 0, false, errQBlockBadOption
	}
	if !b.validQ2Family(r) {
		return 0, 0, false, errQBlockBadOption
	}
	value, err := r.GetOptionUint32(message.QBlock2)
	if err != nil {
		return 0, 0, false, ErrInvalidOptionBlock2
	}
	szx, num, more, err := DecodeQBlockOption(value)
	if err != nil || szx > qBlockMaxSZX {
		return 0, 0, false, errQBlockBadOption
	}
	if _, _, requestedErr := b.q2RequestedBlocks(r, szx, qBlockMaxNUM+1); requestedErr != nil {
		return 0, 0, false, requestedErr
	}
	if r.HasOption(message.Observe) {
		observe, observeErr := r.Observe()
		if observeErr != nil || observe > 1 || num != 0 || optionCount(r, message.QBlock2) != 1 {
			return 0, 0, false, errQBlockBadOption
		}
	}
	return szx, num, more, nil
}

func (b *Engine[C]) validQ2Family(r *pool.Message) bool {
	return optionCount(r, message.QBlock2) <= int(b.config.MaxPayloads) && !r.HasOption(message.Block1) &&
		!r.HasOption(message.Block2) && !r.HasOption(message.QBlock1)
}

func (b *Engine[C]) q2ResponseKey(r *pool.Message, remoteAddr net.Addr, num int64, more bool) (string, error) {
	key := q2TransferKey(r, remoteAddr)
	identity := qTransferKey(r, remoteAddr, nil, true)
	b.q2SendMu.Lock()
	if b.q2Senders[key] == nil && (num != 0 || !more || r.HasOption(message.RequestTag) || r.Code() != codes.GET) {
		found, err := b.findQ2ResponseLocked(identity)
		if err != nil {
			b.q2SendMu.Unlock()
			return "", err
		}
		if found != "" {
			key = found
		}
	}
	if transfer := b.q2Senders[key]; transfer != nil && transfer.mode != r.Type() {
		b.q2SendMu.Unlock()
		return "", errQBlockBadOption
	}
	b.q2SendMu.Unlock()
	return key, nil
}

func (b *Engine[C]) findQ2ResponseLocked(identity string) (string, error) {
	found := ""
	for key, transfer := range b.q2Senders {
		if transfer.identity != identity || !transfer.expires.After(time.Now()) {
			continue
		}
		if found != "" {
			return "", ErrInvalidOptionBlock2
		}
		found = key
	}
	return found, nil
}

func (b *Engine[C]) loadQ2Response(w *responsewriter.ResponseWriter[C], r *pool.Message, remote net.Addr,
	key string, szx SZX, num int64, next func(*responsewriter.ResponseWriter[C], *pool.Message),
) (*pool.Message, error) {
	response, err := b.buildQBlock2Response(r.Context(), key, r.Token(), szx, num)
	if !errors.Is(err, errQBlock2ResponseNotFound) {
		return response, err
	}
	if num != 0 {
		return nil, ErrQBlockInvalidNUM
	}
	next(w, r)
	if w.Message().Code() < codes.Created {
		return w.Message(), nil
	}
	if w.Message().Body() == nil {
		w.Message().SetBody(bytes.NewReader(nil))
	}
	if cacheErr := b.cacheQBlock2Response(r.Context(), key, qTransferKey(r, remote, nil, true), w.Message(), r.Type()); cacheErr != nil {
		return nil, cacheErr
	}
	if observe, observeErr := r.Observe(); observeErr == nil && observe == 0 && w.Message().HasOption(message.Observe) {
		if registerErr := b.registerQObservation(r, remote, szx); registerErr != nil {
			return nil, registerErr
		}
	}
	return b.buildQBlock2Response(r.Context(), key, r.Token(), szx, num)
}

func (b *Engine[C]) prepareQ2NonSend(r *pool.Message, key string, szx SZX, nums []int64, count int64, wholeBody bool) (bool, error) {
	num := nums[0]
	if wholeBody {
		b.q2SendMu.Lock()
		if transfer := b.q2Senders[key]; transfer != nil {
			// A delayed Continue must not retransmit a set already sent.
			if num != 0 && num <= transfer.sentThrough {
				b.q2SendMu.Unlock()
				return true, nil
			}
			transfer.sentThrough = num
		}
		b.q2SendMu.Unlock()
	}
	transport, ok := any(b.cc).(Transport)
	if !ok {
		return false, errors.New("Q-Block burst transport is unavailable")
	}
	b.q2SendMu.Lock()
	transfer := b.q2Senders[key]
	if transfer != nil && !wholeBody {
		deadline := time.Now().Add(transfer.delay)
		if deadline.After(transfer.recoveryUntil) {
			transfer.recoveryUntil = deadline
		}
	}
	var recoveryUntil time.Time
	if transfer != nil {
		recoveryUntil = transfer.recoveryUntil
	}
	b.q2SendMu.Unlock()
	transport.DelayQBlock("q2:"+key, recoveryUntil)
	if wholeBody && recoveryUntil.After(time.Now()) {
		return true, b.scheduleQ2Set(r.Context(), &q2SetJob[C]{
			transport: transport, key: key, token: r.Token(), szx: szx, mode: r.Type(),
			nums: nums, count: count, wholeBody: true,
		}, recoveryUntil)
	}
	return false, nil
}

func (b *Engine[C]) continueQ2Response(r *pool.Message, key string, szx SZX, nums []int64, count int64, wholeBody bool) error {
	if len(nums) > 1 || wholeBody && nums[len(nums)-1]+1 < count {
		transport, ok := any(b.cc).(Transport)
		if !ok {
			return errors.New("Q-Block burst transport is unavailable")
		}
		// The first payload is piggybacked by the caller. Subsequent payloads
		// share its reliability mode and pass through the connection worker.
		token := r.Token()
		mode := r.Type()
		remaining := slices.Clone(nums[1:])
		delay := time.Duration(0)
		if len(remaining) == 0 && wholeBody {
			next := nums[0] + 1
			for num := next; num <= b.config.setLast(next, count); num++ {
				remaining = append(remaining, num)
			}
		}
		if mode == message.NonConfirmable && len(remaining) > 0 && remaining[0]/b.config.MaxPayloads != nums[0]/b.config.MaxPayloads {
			delay = b.q2SetDelay(key)
		}
		return b.scheduleQ2Set(r.Context(), &q2SetJob[C]{
			transport: transport, key: key, token: token, szx: szx, mode: mode, nums: remaining,
			count: count, wholeBody: wholeBody, recovery: !wholeBody,
		}, time.Now().Add(delay))
	}
	return nil
}

func (b *Engine[C]) q2RequestedBlocks(r *pool.Message, szx SZX, count int64) ([]int64, bool, error) {
	var nums []int64
	previous := int64(-1)
	wholeBody := false
	for _, option := range r.Options() {
		if option.ID != message.QBlock2 {
			continue
		}
		value, _, err := message.DecodeUint32(option.Value)
		if err != nil {
			return nil, false, err
		}
		optionSZX, num, more, err := DecodeQBlockOption(value)
		if err != nil || optionSZX != szx || num <= previous || num >= count {
			return nil, false, ErrInvalidOptionBlock2
		}
		previous = num
		last := num
		if more {
			last = b.config.setLast(num, count)
			if num%b.config.MaxPayloads == 0 && optionCount(r, message.QBlock2) == 1 {
				wholeBody = true
			}
		}
		nums = appendQ2Range(nums, num, last)
	}
	return nums, wholeBody, nil
}

func appendQ2Range(nums []int64, first, last int64) []int64 {
	if len(nums) > 0 {
		first = max(first, nums[len(nums)-1]+1)
	}
	for num := first; num <= last; num++ {
		nums = append(nums, num)
	}
	return nums
}

func (b *Engine[C]) q2SetDelay(key string) time.Duration {
	b.q2SendMu.Lock()
	defer b.q2SendMu.Unlock()
	if transfer := b.q2Senders[key]; transfer != nil {
		return transfer.delay
	}
	return b.config.NonTimeout
}

var errQBlock2ResponseNotFound = errors.New("Q-Block2 response representation not found")

func (b *Engine[C]) buildQBlock2Response(ctx context.Context, key string, token message.Token, szx SZX, num int64) (*pool.Message, error) {
	return b.buildQBlock2ResponseFrom(ctx, key, token, szx, num, nil)
}

func (b *Engine[C]) buildQBlock2ResponseFrom(ctx context.Context, key string, token message.Token, szx SZX, num int64, expected *q2SendTransfer) (*pool.Message, error) {
	b.q2SendMu.Lock()
	defer b.q2SendMu.Unlock()
	transfer := b.q2Senders[key]
	if transfer == nil || expected != nil && transfer != expected {
		return nil, errQBlock2ResponseNotFound
	}
	if !transfer.expires.After(time.Now()) {
		delete(b.q2Senders, key)
		b.q2SendBytes -= transfer.size
		b.qReleaseBytes(transfer.size)
		b.qReleaseTransfer()
		b.cc.ReleaseMessage(transfer.message)
		return nil, errQBlock2ResponseNotFound
	}
	response := b.cc.AcquireMessage(ctx)
	response.SetCode(transfer.message.Code())
	response.SetToken(token)
	response.SetType(transfer.message.Type())
	response.SetMessageID(transfer.message.MessageID())
	response.ResetOptionsTo(transfer.message.Options())
	size, err := transfer.message.BodySize()
	if err != nil {
		b.cc.ReleaseMessage(response)
		return nil, err
	}
	sender, err := NewSender(transfer.message.Body(), szx, size)
	if err != nil {
		b.cc.ReleaseMessage(response)
		return nil, err
	}
	payload, more, err := sender.Block(num)
	if err != nil {
		b.cc.ReleaseMessage(response)
		return nil, err
	}
	option, err := EncodeQBlockOption(szx, num, more)
	if err != nil {
		b.cc.ReleaseMessage(response)
		return nil, err
	}
	response.SetOptionUint32(message.QBlock2, option)
	response.SetOptionUint32(message.Size2, uint32(size)) // #nosec G115 -- cached representation is bounded to 16 MiB.
	response.SetBody(bytes.NewReader(payload))
	return response, nil
}

func (b *Engine[C]) cacheQBlock2Response(ctx context.Context, key, identity string, response *pool.Message, mode message.Type) error {
	return b.cacheQBlock2Notification(ctx, key, identity, response, mode, nil)
}

func (b *Engine[C]) cacheQBlock2Notification(ctx context.Context, key, identity string, response *pool.Message, mode message.Type, generation *qObservation) error {
	size, err := response.BodySize()
	if err != nil {
		return err
	}
	if size < 0 {
		return ErrQBlockInvalidSize
	}
	if size > q1DefaultMaxBodySize {
		return ErrQBlockTooLarge
	}
	if !b.qReserveBytes(size) {
		return ErrQBlockTooLarge
	}
	if !b.qReserveTransfer() {
		b.qReleaseBytes(size)
		return ErrQBlockTooLarge
	}
	retained := false
	defer func() {
		if !retained {
			b.qReleaseBytes(size)
			b.qReleaseTransfer()
		}
	}()
	stored, err := b.snapshotQ2Body(ctx, response, size)
	if err != nil {
		return err
	}
	etag, _ := stored.GetOptionBytes(message.ETag)
	response.SetOptionBytes(message.ETag, etag)
	stored.SetType(mode)
	b.q2SendMu.Lock()
	if err := qClosedError(b.q2Closed); err != nil {
		b.q2SendMu.Unlock()
		b.cc.ReleaseMessage(stored)
		return err
	}
	if err := b.validateQNotificationLocked(generation); err != nil {
		b.q2SendMu.Unlock()
		b.cc.ReleaseMessage(stored)
		return err
	}
	if old := b.q2Senders[key]; old != nil {
		b.q2SendBytes -= old.size
		b.qReleaseBytes(old.size)
		b.qReleaseTransfer()
		delete(b.q2Senders, key)
		b.cc.ReleaseMessage(old.message)
	} else if len(b.q2Senders) >= 4096 {
		b.q2SendMu.Unlock()
		b.cc.ReleaseMessage(stored)
		return ErrQBlockTooLarge
	}
	b.q2SendBytes += size
	retained = true
	maxAge, maxAgeErr := response.GetOptionUint32(message.MaxAge)
	if maxAgeErr != nil {
		maxAge = 60
	}
	lifetime := min(b.config.NonPartialTimeout(), max(b.config.NonReceiveTimeout, time.Duration(maxAge)*time.Second))
	b.q2Senders[key] = &q2SendTransfer{
		message: stored, expires: time.Now().Add(lifetime),
		size: size, identity: identity, mode: mode, sentThrough: -1, delay: b.config.nonDelay(),
	}
	observePresent := response.HasOption(message.Observe)
	if generation != nil && !observePresent {
		b.q2Senders[key].terminalObservationKey = generation.registrationKey
	}
	b.finishQNotificationLocked(generation, observePresent)
	b.q2SendMu.Unlock()
	return nil
}

func (b *Engine[C]) snapshotQ2Body(ctx context.Context, response *pool.Message, size int64) (*pool.Message, error) {
	payload, err := copyQ2Body(response.Body(), size)
	if err != nil {
		return nil, err
	}
	stored := b.cloneMessage(response)
	stored.SetContext(ctx)
	stored.SetBody(bytes.NewReader(payload))
	if err := ensureQ2ETag(stored); err != nil {
		b.cc.ReleaseMessage(stored)
		return nil, err
	}
	return stored, nil
}

func copyQ2Body(body io.ReadSeeker, size int64) ([]byte, error) {
	if body == nil && size != 0 {
		return nil, ErrQBlockInvalidSize
	}
	payload := make([]byte, int(size))
	if body == nil {
		return payload, nil
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	_, readErr := io.ReadFull(body, payload)
	var extra [1]byte
	n, extraErr := io.ReadFull(body, extra[:])
	_, seekErr := body.Seek(0, io.SeekStart)
	if readErr != nil || n != 0 || !errors.Is(extraErr, io.EOF) || seekErr != nil {
		return nil, ErrQBlockInvalidSize
	}
	return payload, nil
}

func ensureQ2ETag(response *pool.Message) error {
	if response.HasOption(message.ETag) {
		etag, err := response.GetOptionBytes(message.ETag)
		if err != nil || optionCount(response, message.ETag) != 1 || len(etag) == 0 || len(etag) > 8 {
			return ErrQBlockInvalidSize
		}
		return nil
	}
	if response.Body() == nil {
		response.SetOptionBytes(message.ETag, []byte{0})
		return nil
	}
	body := response.Body()
	if _, err := body.Seek(0, 0); err != nil {
		return err
	}
	size, err := response.BodySize()
	if err != nil {
		return err
	}
	if size > q1DefaultMaxBodySize {
		return ErrQBlockTooLarge
	}
	hash := sha256.New()
	n, readErr := io.Copy(hash, io.LimitReader(body, size+1))
	if _, err := body.Seek(0, 0); err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if n != size {
		return ErrQBlockInvalidSize
	}
	response.SetOptionBytes(message.ETag, hash.Sum(nil)[:8])
	return nil
}

func (b *Engine[C]) dispatchQBlock2(
	w *responsewriter.ResponseWriter[C],
	r *pool.Message,
	maxSZX SZX,
	remoteAddr net.Addr,
	next func(w *responsewriter.ResponseWriter[C], r *pool.Message),
) {
	err := b.handleQBlock2(w, r, min(maxSZX, qBlockMaxSZX), remoteAddr, next)
	if err != nil {
		w.Message().SetBody(nil)
		code := codes.BadRequest
		switch {
		case errors.Is(err, ErrQBlockTooLarge):
			code = codes.RequestEntityTooLarge
		case errors.Is(err, errQBlockBadOption):
			code = codes.BadOption
		case errors.Is(err, ErrQBlockScheduleFull):
			code = codes.ServiceUnavailable
		}
		if setErr := w.SetResponse(code, message.TextPlain, nil); setErr != nil {
			b.errors(fmt.Errorf("cannot send Q-Block2 error response: %w", setErr))
		}
		b.errors(fmt.Errorf("handle Q-Block2 request(%v): %w", r, err))
	}
}
