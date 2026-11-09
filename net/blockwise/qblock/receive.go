package qblock

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

const (
	q1DefaultMaxBodySize = 16 << 20
	q1MaxTrackedBlocks   = 1 << 16
)

var errQBlockBadOption = errors.New("invalid Q-Block option semantics")

type q1ReceiveTransfer struct {
	assembly           *Assembly
	expires            time.Time
	completed          bool
	bytes              int64
	bodySize           int64
	szx                SZX
	response           *pool.Message
	token              message.Token
	initialToken       message.Token
	mode               message.Type
	lastValue          uint32
	retries            int
	updated            time.Time
	reportedMissing    []uint32
	reportedAt         time.Time
	continuedThrough   int64
	continues          map[int64]uint32
	completionSequence uint64
	completionToken    string
}

func q1TransferKey(r *pool.Message, remoteAddr net.Addr, tag []byte) string {
	return qTransferKey(r, remoteAddr, tag, false)
}

func qTransferKey(r *pool.Message, remoteAddr net.Addr, tag []byte, response bool) string {
	h := &bytes.Buffer{}
	endpoint := ""
	if remoteAddr != nil {
		endpoint = remoteAddr.String()
	}
	blocks.WriteExchangeKeyPart(h, []byte(endpoint))
	var code [2]byte
	binary.BigEndian.PutUint16(code[:], uint16(r.Code()))
	_, _ = h.Write(code[:])
	for _, option := range r.Options() {
		if option.ID == message.QBlock1 || option.ID == message.QBlock2 ||
			option.ID == message.Size1 || option.ID == message.Block1 || option.ID == message.Block2 {
			continue
		}
		if response && (option.ID == message.Observe || option.ID == message.ETag) {
			continue
		}
		if option.ID&0x1f == 0x1c {
			continue
		}
		var optionHeader [10]byte
		binary.BigEndian.PutUint16(optionHeader[:2], uint16(option.ID))
		binary.BigEndian.PutUint64(optionHeader[2:], uint64(len(option.Value)))
		_, _ = h.Write(optionHeader[:])
		_, _ = h.Write(option.Value)
	}
	blocks.WriteExchangeKeyPart(h, tag)
	return h.String()
}

func optionCount(r *pool.Message, id message.OptionID) int {
	count := 0
	for _, option := range r.Options() {
		if option.ID == id {
			count++
		}
	}
	return count
}

func validateQBlock1(r *pool.Message) error {
	if !qBodyMethod(r.Code()) {
		return errQBlockBadOption
	}
	if optionCount(r, message.QBlock1) != 1 || r.HasOption(message.Block1) || r.HasOption(message.Block2) {
		return fmt.Errorf("%w: illegal Q-Block1 option set", errQBlockBadOption)
	}
	if r.HasOption(message.QBlock2) {
		if optionCount(r, message.QBlock2) != 1 {
			return fmt.Errorf("%w: illegal Q-Block2 capability indication", errQBlockBadOption)
		}
		q2Value, q2Err := r.GetOptionUint32(message.QBlock2)
		q2SZX, q2NUM, _, decodeErr := DecodeQBlockOption(q2Value)
		if q2Err != nil || decodeErr != nil || q2SZX > qBlockMaxSZX || q2NUM != 0 {
			return fmt.Errorf("%w: invalid Q-Block2 capability indication", errQBlockBadOption)
		}
	}
	if !r.HasOption(message.RequestTag) || !r.HasOption(message.Size1) {
		return fmt.Errorf("%w: one Request-Tag and Size1 are required", ErrInvalidOptionBlock1)
	}
	if optionCount(r, message.RequestTag) != 1 || optionCount(r, message.Size1) != 1 {
		return errQBlockBadOption
	}
	return nil
}

type q1Parameters struct {
	tag      []byte
	value    uint32
	szx      SZX
	num      int64
	more     bool
	bodySize uint32
}

func parseQBlock1(r *pool.Message) (q1Parameters, error) {
	var p q1Parameters
	if err := validateQBlock1(r); err != nil {
		return p, err
	}
	tags := make([][]byte, 1)
	count, err := r.RequestTags(tags)
	if err != nil || count != 1 {
		return p, fmt.Errorf("%w: exactly one Request-Tag required", ErrInvalidOptionBlock1)
	}
	p.tag, err = r.RequestTag()
	if err != nil || len(p.tag) > 8 {
		return p, fmt.Errorf("%w: invalid Request-Tag", ErrInvalidOptionBlock1)
	}
	p.value, err = r.GetOptionUint32(message.QBlock1)
	if err != nil {
		return p, fmt.Errorf("%w: invalid Q-Block1 value", ErrInvalidOptionBlock1)
	}
	p.szx, p.num, p.more, err = DecodeQBlockOption(p.value)
	if err != nil || p.szx > qBlockMaxSZX {
		return p, fmt.Errorf("%w: invalid SZX or NUM", errQBlockBadOption)
	}
	p.bodySize, err = r.GetOptionUint32(message.Size1)
	if err != nil {
		return p, fmt.Errorf("%w: invalid Size1", errQBlockBadOption)
	}
	return p, nil
}

// q1TransferLocked creates or validates a transfer while q1ReceiveMu is held.
func (b *Engine[C]) q1TransferLocked(r *pool.Message, key string, p q1Parameters) (*q1ReceiveTransfer, error) {
	if b.q1Closed {
		return nil, context.Canceled
	}
	transfer := b.q1Receivers[key]
	if transfer != nil && !transfer.expires.After(time.Now()) {
		b.removeExpiredQ1TransferLocked(key, transfer)
		transfer = nil
	}
	if transfer != nil {
		if transfer.bodySize != int64(p.bodySize) || transfer.szx != p.szx || transfer.mode != r.Type() {
			return nil, ErrInvalidOptionBlock1
		}
		return transfer, nil
	}
	return b.newQ1TransferLocked(r, key, p)
}

func (b *Engine[C]) removeExpiredQ1TransferLocked(key string, transfer *q1ReceiveTransfer) {
	delete(b.q1Receivers, key)
	b.q1ReceiveBytes -= transfer.bytes
	b.qReleaseBytes(transfer.bytes)
	b.qReleaseTransfer()
	if transfer.response != nil {
		b.cc.ReleaseMessage(transfer.response)
	}
}

func (b *Engine[C]) newQ1TransferLocked(r *pool.Message, key string, p q1Parameters) (*q1ReceiveTransfer, error) {
	maxBodySize := b.maxReceiveBodySize
	if maxBodySize <= 0 {
		maxBodySize = q1DefaultMaxBodySize
	}
	maxEntries := b.receivingMessagesCacheMaxEntries.Load()
	if len(b.q1Receivers) >= 4096 || maxEntries > 0 && int64(len(b.q1Receivers)) >= maxEntries {
		return nil, errReceivingMessagesCacheFull
	}
	assembly, err := NewAssembly(p.szx, int64(p.bodySize), maxBodySize, q1MaxTrackedBlocks)
	if err != nil {
		return nil, err
	}
	if !b.qReserveTransfer() {
		return nil, errReceivingMessagesCacheFull
	}
	transfer := &q1ReceiveTransfer{
		assembly: assembly, bodySize: int64(p.bodySize), szx: p.szx, mode: r.Type(),
		initialToken: bytes.Clone(r.Token()), continuedThrough: -1,
	}
	b.q1Receivers[key] = transfer
	return transfer, nil
}

func (b *Engine[C]) handleQBlock1(
	w *responsewriter.ResponseWriter[C],
	r *pool.Message,
	maxSZX SZX,
	remoteAddr net.Addr,
	next func(w *responsewriter.ResponseWriter[C], r *pool.Message),
) error {
	p, err := parseQBlock1(r)
	if err != nil {
		return err
	}
	value := p.value
	key := q1TransferKey(r, remoteAddr, p.tag)
	b.q1ReceiveMu.Lock()
	transfer, err := b.q1TransferLocked(r, key, p)
	if err != nil {
		b.q1ReceiveMu.Unlock()
		return err
	}
	if transfer.completed {
		replayErr := b.replayQ1Completed(w, r, transfer)
		b.q1ReceiveMu.Unlock()
		return replayErr
	}
	transfer.expires = time.Now().Add(b.config.NonPartialTimeout())
	transfer.token = bytes.Clone(r.Token())
	if p.num == 0 {
		transfer.initialToken = bytes.Clone(r.Token())
	}
	transfer.lastValue = value
	b.q1ReceiveMu.Unlock()

	payload, err := readQBody(r)
	if err != nil {
		return err
	}
	b.q1ReceiveMu.Lock()
	if !b.q1Closed && b.q1Receivers[key] == transfer && transfer.completed {
		replayErr := b.replayQ1Completed(w, r, transfer)
		b.q1ReceiveMu.Unlock()
		return replayErr
	}
	if b.q1Closed || b.q1Receivers[key] != transfer || transfer.assembly == nil {
		b.q1ReceiveMu.Unlock()
		return context.Canceled
	}
	assembly := transfer.assembly
	complete, duplicate, err := b.addQ1PayloadLocked(key, transfer, assembly, p, payload)
	b.q1ReceiveMu.Unlock()
	if err != nil {
		return err
	}
	if !complete {
		return b.respondQ1Incomplete(w, r, key, transfer, assembly, p, duplicate)
	}
	return b.completeQ1(w, r, key, transfer, maxSZX, remoteAddr, next)
}

func (b *Engine[C]) addQ1PayloadLocked(key string, transfer *q1ReceiveTransfer, assembly *Assembly,
	p q1Parameters, payload []byte,
) (bool, bool, error) {
	if !b.qReserveBytes(int64(len(payload))) {
		return false, false, ErrQBlockTooLarge
	}
	b.q1ReceiveBytes += int64(len(payload))
	transfer.bytes += int64(len(payload))
	firstMissingBefore, _ := assembly.FirstMissing()
	complete, duplicate, err := assembly.Add(p.num, p.more, payload)
	if !duplicate && err == nil {
		transfer.updated = time.Now()
		if p.num == firstMissingBefore {
			transfer.retries = 0
		}
	}
	if duplicate || err != nil {
		b.q1ReceiveBytes -= int64(len(payload))
		transfer.bytes -= int64(len(payload))
		b.qReleaseBytes(int64(len(payload)))
	}
	if err != nil {
		delete(b.q1Receivers, key)
		b.q1ReceiveBytes -= transfer.bytes
		b.qReleaseBytes(transfer.bytes)
		b.qReleaseTransfer()
		return false, duplicate, err
	}
	return complete, duplicate, nil
}

func (b *Engine[C]) respondQ1Incomplete(w *responsewriter.ResponseWriter[C], r *pool.Message, key string,
	transfer *q1ReceiveTransfer, assembly *Assembly, p q1Parameters, duplicate bool,
) error {
	firstMissing, exists := assembly.FirstMissing()
	if !exists {
		return nil
	}
	if r.Type() != message.NonConfirmable {
		return nil
	}
	if err := b.scheduleQ1Missing(r.Context(), key); err != nil {
		return err
	}
	if duplicate {
		b.q1ReceiveMu.Lock()
		reply, exists := transfer.continues[p.num]
		b.q1ReceiveMu.Unlock()
		if exists {
			return respondQBlock1(w, codes.Continue, reply, nil)
		}
		return nil
	}
	if p.num/b.config.MaxPayloads > firstMissing/b.config.MaxPayloads {
		return b.respondQ1Missing(w, transfer, assembly, p.value)
	}
	if firstMissing <= 0 || firstMissing%b.config.MaxPayloads != 0 {
		return nil
	}
	// A NON Continue confirms a complete set even when an earlier lost
	// payload, rather than its last NUM, completed it.
	confirmed, _ := EncodeQBlockOption(p.szx, firstMissing-1, true)
	b.q1ReceiveMu.Lock()
	if transfer.continuedThrough >= firstMissing-1 {
		b.q1ReceiveMu.Unlock()
		return nil
	}
	transfer.continuedThrough = firstMissing - 1
	if transfer.continues == nil {
		transfer.continues = make(map[int64]uint32)
	}
	transfer.continues[p.num] = confirmed
	b.q1ReceiveMu.Unlock()
	return respondQBlock1(w, codes.Continue, confirmed, nil)
}

func (b *Engine[C]) respondQ1Missing(w *responsewriter.ResponseWriter[C], transfer *q1ReceiveTransfer,
	assembly *Assembly, value uint32,
) error {
	missing := assembly.missingSet(b.config.MaxPayloads)
	reported, report, err := b.encodeQ1MissingReport(missing, w.Message().Token(), value)
	if err != nil {
		return err
	}
	b.q1ReceiveMu.Lock()
	sameReport := len(transfer.reportedMissing) == len(reported)
	if sameReport {
		for i := range reported {
			if transfer.reportedMissing[i] != reported[i] {
				sameReport = false
				break
			}
		}
	}
	suppress := sameReport && time.Since(transfer.reportedAt) < b.config.NonReceiveTimeout
	if !suppress {
		transfer.reportedMissing = append(transfer.reportedMissing[:0], reported...)
		transfer.reportedAt = time.Now()
	}
	b.q1ReceiveMu.Unlock()
	if suppress {
		return nil
	}
	return respondQBlock1(w, codes.RequestEntityIncomplete, value, report)
}

func (b *Engine[C]) encodeQ1MissingReport(missing []uint32, token message.Token, value uint32) ([]uint32, []byte, error) {
	const maxCoAPMessageSize = 1<<16 - 1
	maxDatagramSize := uint32(maxCoAPMessageSize)
	if provider, ok := any(b.cc).(interface{ QBlockMaxDatagramSize() uint32 }); ok {
		if size := provider.QBlockMaxDatagramSize(); size > 0 {
			maxDatagramSize = size
		}
	}
	// CoAP header, token, Content-Format option, Q-Block1 option, and payload
	// marker. The connection's datagram limit already reserves DTLS overhead.
	envelopeSize := uint32(10) + uint32(len(token)) + coapUintSize(value) // #nosec G115 -- CoAP tokens are at most 8 bytes.
	if envelopeSize >= maxDatagramSize {
		return nil, nil, ErrQBlockTooLarge
	}
	payloadLimit := int(maxDatagramSize - envelopeSize)
	reported := make([]uint32, 0, len(missing))
	var report []byte
	for _, block := range missing {
		encoded, err := EncodeMissingBlocks(nil, []uint32{block})
		if err != nil {
			return nil, nil, err
		}
		if len(report)+len(encoded) > payloadLimit {
			break
		}
		report = append(report, encoded...)
		reported = append(reported, block)
	}
	if len(reported) == 0 {
		return nil, nil, ErrQBlockTooLarge
	}
	return reported, report, nil
}

func coapUintSize(value uint32) uint32 {
	switch {
	case value == 0:
		return 0
	case value <= 0xff:
		return 1
	case value <= 0xffff:
		return 2
	default:
		return 3
	}
}

func (b *Engine[C]) completeQ1(w *responsewriter.ResponseWriter[C], r *pool.Message, key string,
	transfer *q1ReceiveTransfer, maxSZX SZX, remoteAddr net.Addr,
	next func(*responsewriter.ResponseWriter[C], *pool.Message),
) error {
	b.q1ReceiveMu.Lock()
	if b.q1Receivers[key] != transfer {
		b.q1ReceiveMu.Unlock()
		return context.Canceled
	}
	if transfer.completed {
		b.q1ReceiveMu.Unlock()
		return nil
	}
	transfer.completed = true
	assembly := transfer.assembly
	transfer.completionSequence = r.Sequence()
	transfer.completionToken = string(r.Token())
	b.q1ReceiveMu.Unlock()
	if transport, ok := any(b.cc).(Transport); ok {
		transport.CancelQBlock("q1:" + key)
	}

	q2Request, err := b.prepareQ1ResponseRequest(w, r, transfer, assembly, next)
	if err != nil {
		return err
	}
	if err := b.completeQ1Response(w, r, transfer, q2Request, maxSZX, remoteAddr, next); err != nil {
		return err
	}
	return b.storeQ1Response(w, r, key, transfer)
}

func (b *Engine[C]) prepareQ1ResponseRequest(w *responsewriter.ResponseWriter[C], r *pool.Message,
	transfer *q1ReceiveTransfer, assembly *Assembly,
	next func(*responsewriter.ResponseWriter[C], *pool.Message),
) (*pool.Message, error) {
	body, err := assembly.ReadSeeker()
	if err != nil {
		return nil, err
	}
	request := b.cloneMessage(r)
	request.Remove(message.QBlock1)
	request.Remove(message.Size1)
	if observe, observeErr := r.Observe(); observeErr == nil && observe == 0 && r.HasOption(message.QBlock2) {
		request.SetToken(transfer.initialToken)
	}
	request.SetBody(body)
	next(w, request)
	q2Request := b.cloneMessage(request)
	q2Request.SetBody(nil)
	b.cc.ReleaseMessage(request)
	return q2Request, nil
}

func (b *Engine[C]) completeQ1Response(w *responsewriter.ResponseWriter[C], r *pool.Message,
	transfer *q1ReceiveTransfer, q2Request *pool.Message, maxSZX SZX, remoteAddr net.Addr,
	next func(*responsewriter.ResponseWriter[C], *pool.Message),
) error {
	defer b.cc.ReleaseMessage(q2Request)
	responseSize, sizeErr := w.Message().BodySize()
	b.ensureQBlock2Capability(q2Request, transfer.szx)
	if sizeErr == nil && responseSize > transfer.szx.Size() {
		if err := b.prepareQBlock2Response(w, r, q2Request, maxSZX, remoteAddr, next); err != nil {
			return err
		}
	}
	if err := b.registerCompletedQ1Observation(w, q2Request, transfer, remoteAddr); err != nil {
		return err
	}
	return nil
}

func (b *Engine[C]) ensureQBlock2Capability(request *pool.Message, szx SZX) {
	if request.HasOption(message.QBlock2) {
		return
	}
	capability, _ := EncodeQBlockOption(szx, 0, true)
	request.SetOptionUint32(message.QBlock2, capability)
}

func (b *Engine[C]) prepareQBlock2Response(
	w *responsewriter.ResponseWriter[C], r, q2Request *pool.Message, maxSZX SZX, remoteAddr net.Addr,
	next func(*responsewriter.ResponseWriter[C], *pool.Message),
) error {
	if observe, observeErr := q2Request.Observe(); observeErr == nil && observe == 0 {
		b.CancelQBlockObservation(q2Request.Token(), remoteAddr)
	}
	if err := b.cacheQBlock2Response(r.Context(), q2TransferKey(q2Request, remoteAddr),
		qTransferKey(q2Request, remoteAddr, nil, true), w.Message(), r.Type()); err != nil {
		return err
	}
	return b.handleQBlock2(w, q2Request, maxSZX, remoteAddr, next)
}

func (b *Engine[C]) registerCompletedQ1Observation(
	w *responsewriter.ResponseWriter[C], q2Request *pool.Message, transfer *q1ReceiveTransfer, remoteAddr net.Addr,
) error {
	if !w.Message().HasOption(message.Observe) {
		return nil
	}
	observe, err := q2Request.Observe()
	if err != nil {
		return err
	}
	if observe != 0 {
		return nil
	}
	return b.registerQObservation(q2Request, remoteAddr, transfer.szx)
}

func (b *Engine[C]) storeQ1Response(w *responsewriter.ResponseWriter[C], r *pool.Message, key string,
	transfer *q1ReceiveTransfer,
) error {
	stored := b.cc.AcquireMessage(r.Context())
	if err := w.Message().Clone(stored); err != nil {
		b.cc.ReleaseMessage(stored)
		return err
	}
	b.q1ReceiveMu.Lock()
	if b.q1Receivers[key] != transfer {
		b.q1ReceiveMu.Unlock()
		b.cc.ReleaseMessage(stored)
		return nil
	}
	transfer.response = stored
	// Sparse chunks are no longer retained after delivering the body. The
	// completed marker and small final response suppress application replay.
	b.q1ReceiveBytes -= transfer.bytes
	b.qReleaseBytes(transfer.bytes)
	transfer.bytes = 0
	transfer.assembly = nil
	transfer.continues = nil
	b.q1ReceiveMu.Unlock()
	return nil
}

func (b *Engine[C]) scheduleQ1Missing(ctx context.Context, key string) error {
	transport, ok := any(b.cc).(Transport)
	if !ok {
		return errors.New("Q-Block timer transport unavailable")
	}
	b.q1ReceiveMu.Lock()
	transfer := b.q1Receivers[key]
	if transfer == nil || transfer.completed {
		b.q1ReceiveMu.Unlock()
		return nil
	}
	deadline := transfer.updated.Add(b.config.NonReceiveTimeout << transfer.retries)
	b.q1ReceiveMu.Unlock()
	return transport.ScheduleQBlock("q1:"+key, deadline, func() error {
		b.q1ReceiveMu.Lock()
		transfer := b.q1Receivers[key]
		if transfer == nil || transfer.completed {
			b.q1ReceiveMu.Unlock()
			return nil
		}
		deadline := transfer.updated.Add(b.config.NonReceiveTimeout << transfer.retries)
		if time.Now().Before(deadline) {
			b.q1ReceiveMu.Unlock()
			return b.scheduleQ1Missing(ctx, key)
		}
		if !transfer.expires.After(time.Now()) || transfer.retries >= b.config.NonMaxRetransmit {
			delete(b.q1Receivers, key)
			b.q1ReceiveBytes -= transfer.bytes
			b.qReleaseBytes(transfer.bytes)
			b.qReleaseTransfer()
			b.q1ReceiveMu.Unlock()
			return nil
		}
		nums := transfer.assembly.missingSet(b.config.MaxPayloads)
		token := bytes.Clone(transfer.token)
		value := transfer.lastValue
		transfer.retries++
		transfer.updated = time.Now()
		b.q1ReceiveMu.Unlock()
		_, report, err := b.encodeQ1MissingReport(nums, token, value)
		if err != nil {
			return err
		}
		msg := b.cc.AcquireMessage(ctx)
		msg.SetToken(token)
		msg.SetCode(codes.RequestEntityIncomplete)
		msg.SetType(message.NonConfirmable)
		msg.SetContentFormat(message.AppMissingBlocksCBORSeq)
		msg.SetOptionUint32(message.QBlock1, value)
		msg.SetBody(bytes.NewReader(report))
		err = transport.SendQBlock(msg)
		b.cc.ReleaseMessage(msg)
		if err != nil {
			return err
		}
		return b.scheduleQ1Missing(ctx, key)
	})
}

func respondQBlock1[C responsewriter.Client](
	w *responsewriter.ResponseWriter[C],
	code codes.Code,
	block uint32,
	report []byte,
) error {
	format := message.TextPlain
	var body io.ReadSeeker
	if report != nil {
		format = message.AppMissingBlocksCBORSeq
		body = bytes.NewReader(report)
	}
	if err := w.SetResponse(code, format, body); err != nil {
		return fmt.Errorf("set Q-Block1 response: %w", err)
	}
	w.Message().SetOptionUint32(message.QBlock1, block)
	return nil
}

func (b *Engine[C]) cacheQ1FinalError(r *pool.Message, remoteAddr net.Addr, response *pool.Message) {
	tag, err := r.RequestTag()
	if err != nil {
		return
	}
	key := q1TransferKey(r, remoteAddr, tag)
	b.q1ReceiveMu.Lock()
	defer b.q1ReceiveMu.Unlock()
	transfer := b.q1Receivers[key]
	if transfer == nil || !transfer.completed || transfer.response != nil ||
		transfer.completionSequence != r.Sequence() || transfer.completionToken != string(r.Token()) {
		return
	}
	stored := b.cc.AcquireMessage(r.Context())
	if err := response.Clone(stored); err != nil {
		b.cc.ReleaseMessage(stored)
		return
	}
	transfer.response = stored
	b.q1ReceiveBytes -= transfer.bytes
	b.qReleaseBytes(transfer.bytes)
	transfer.bytes = 0
	transfer.assembly = nil
	transfer.continues = nil
}

func (b *Engine[C]) replayQ1Completed(w *responsewriter.ResponseWriter[C], r *pool.Message, transfer *q1ReceiveTransfer) error {
	if transfer.response == nil {
		return nil
	}
	replay := b.cc.AcquireMessage(r.Context())
	if err := transfer.response.Clone(replay); err != nil {
		b.cc.ReleaseMessage(replay)
		return err
	}
	replay.SetToken(r.Token())
	w.SetMessage(replay)
	return nil
}
