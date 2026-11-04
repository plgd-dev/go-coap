package qblock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/observation"
)

// DoQBlock2 requests a body burst, acknowledges complete payload sets, and
// requests only missing NUMs using repeated Q-Block2 options.
func (b *Engine[C]) DoQBlock2(req *pool.Message, szx SZX,
	_ func(*pool.Message) (*pool.Message, error),
) (*pool.Message, error) {
	transport, ok := any(b.cc).(Transport)
	if !ok {
		return nil, errors.New("Q-Block burst transport is unavailable")
	}
	exchange, err := transport.OpenQBlockExchange(req.Context(), req.Token())
	if err != nil {
		return nil, err
	}
	defer exchange.Close()
	if !req.HasOption(message.RequestTag) {
		request := b.cloneMessage(req)
		defer b.cc.ReleaseMessage(request)
		tag, tagErr := message.GetToken()
		if tagErr != nil {
			return nil, tagErr
		}
		if tagErr := request.SetRequestTag(tag); tagErr != nil {
			return nil, tagErr
		}
		req = request
	}
	return b.ReceiveQBlock2(req, szx, exchange, nil)
}

type q2BodyReceiver[C Client] struct {
	b               *Engine[C]
	req             *pool.Message
	exchange        Exchange
	szx, maxSZX     SZX
	assembly        *Assembly
	etag            []byte
	continued       int64
	partialExpires  time.Time
	retries         int
	requestedSet    int64
	retainedBytes   int64
	sentRequests    int
	observeSequence uint32
	observePresent  bool
	observeTime     time.Time
	bodyType        message.Type
	bodyTypeSet     bool
}

func (s *q2BodyReceiver[C]) send(nums []int64, more bool) error {
	part := s.b.cloneMessage(s.req)
	defer s.b.cc.ReleaseMessage(part)
	if s.sentRequests > 0 {
		token, err := s.exchange.NewToken()
		if err != nil {
			return err
		}
		part.SetToken(token)
		part.Remove(message.Observe)
	}
	s.sentRequests++
	part.Remove(message.QBlock2)
	part.Remove(message.ETag)
	part.Remove(message.QBlock1)
	part.SetBody(nil)
	for _, num := range nums {
		value, err := EncodeQBlockOption(s.szx, num, more)
		if err != nil {
			return err
		}
		part.AddOptionUint32(message.QBlock2, value)
	}
	return s.exchange.Send(part)
}

func (s *q2BodyReceiver[C]) receive() (*pool.Message, error) {
	deadline := time.Now().Add(s.b.config.NonReceiveTimeout << s.retries)
	if !s.partialExpires.IsZero() && s.partialExpires.Before(deadline) {
		deadline = s.partialExpires
	}
	ctx, cancel := context.WithDeadline(s.req.Context(), deadline)
	defer cancel()
	return s.exchange.Receive(ctx)
}

func (s *q2BodyReceiver[C]) recover(receiveErr error) error {
	if s.req.Context().Err() != nil {
		return s.req.Context().Err()
	}
	if errors.Is(receiveErr, context.Canceled) {
		return receiveErr
	}
	if !s.partialExpires.IsZero() && !s.partialExpires.After(time.Now()) {
		return ErrQBlockIncomplete
	}
	if s.retries >= s.b.config.NonMaxRetransmit {
		return fmt.Errorf("Q-Block2 recovery exhausted: %w", receiveErr)
	}
	s.retries++
	if s.assembly == nil {
		return s.send([]int64{0}, true)
	}
	nums := qMissingSet(s.assembly.missingSet(s.b.config.MaxPayloads), s.b.config.MaxPayloads)
	if len(nums) == 0 {
		return ErrQBlockIncomplete
	}
	s.requestedSet = nums[0] / s.b.config.MaxPayloads
	return s.send(nums, false)
}

func (s *q2BodyReceiver[C]) newRepresentation(response *pool.Message, szx SZX, size uint32, etag []byte) error {
	assembly, err := NewAssembly(szx, int64(size), q1DefaultMaxBodySize, q1MaxTrackedBlocks)
	if err != nil {
		return err
	}
	s.szx, s.assembly, s.etag = szx, assembly, bytes.Clone(etag)
	s.bodyTypeSet = false
	if sequence, sequenceErr := response.Observe(); sequenceErr == nil {
		s.observeSequence, s.observePresent, s.observeTime = sequence, true, time.Now()
	}
	maxAge, maxAgeErr := response.GetOptionUint32(message.MaxAge)
	if maxAgeErr != nil {
		maxAge = 60
	}
	s.partialExpires = time.Now().Add(min(s.b.config.NonPartialTimeout(), time.Duration(maxAge)*time.Second))
	return nil
}

// representation checks identity before retaining bytes; older representations
// can still arrive on retained recovery tokens after a newer notification starts.
func (s *q2BodyReceiver[C]) representation(response *pool.Message, szx SZX) (bool, error) {
	size, sizeErr := response.GetOptionUint32(message.Size2)
	etag, etagErr := response.GetOptionBytes(message.ETag)
	if sizeErr != nil || etagErr != nil || len(etag) == 0 || len(etag) > 8 {
		return false, ErrInvalidOptionBlock2
	}
	sequence, sequenceErr := response.Observe()
	changed := s.assembly != nil && (!bytes.Equal(s.etag, etag) || int64(size) != s.assembly.bodySize)
	if changed && s.observePresent {
		newer := sequenceErr == nil && observation.ValidSequenceNumber(s.observeSequence, sequence, s.observeTime, time.Now())
		if !newer {
			return true, nil
		}
		qRetainedBytes.Add(-s.retainedBytes)
		s.retainedBytes, s.assembly = 0, nil
		s.continued, s.retries, s.requestedSet = 0, 0, -1
	}
	if s.assembly == nil {
		return false, s.newRepresentation(response, szx, size, etag)
	}
	if changed || szx != s.szx {
		return false, ErrQBlockConflict
	}
	if s.isNewerSequence(sequence, sequenceErr) {
		s.observeSequence, s.observeTime = sequence, time.Now()
	}
	return false, nil
}

func (s *q2BodyReceiver[C]) isNewerSequence(sequence uint32, err error) bool {
	return s.observePresent && err == nil && sequence != s.observeSequence &&
		observation.ValidSequenceNumber(s.observeSequence, sequence, s.observeTime, time.Now())
}

func (s *q2BodyReceiver[C]) add(response *pool.Message, num int64, more bool) (bool, bool, error) {
	payload, err := readQBody(response)
	if err != nil {
		return false, false, err
	}
	if !qReserveBytes(int64(len(payload))) {
		return false, false, ErrQBlockTooLarge
	}
	complete, duplicate, err := s.assembly.Add(num, more, payload)
	if duplicate || err != nil {
		qRetainedBytes.Add(-int64(len(payload)))
	} else {
		s.retainedBytes += int64(len(payload))
	}
	return complete, duplicate, err
}

func (s *q2BodyReceiver[C]) advance(num int64) error {
	next, _ := s.assembly.FirstMissing()
	resume := s.requestedSet >= 0 && next/s.b.config.MaxPayloads > s.requestedSet
	if resume {
		s.requestedSet, s.retries = -1, 0
	}
	// A completed recovery set must resume the remaining body explicitly:
	// a peer may have stopped its original burst after the missing payloads.
	// Otherwise CON senders advance on ACKs, while NON can accelerate its set.
	if (s.req.Type() == message.NonConfirmable || resume) && next%s.b.config.MaxPayloads == 0 && next > s.continued {
		s.continued = next
		return s.send([]int64{next}, true)
	}
	if num/s.b.config.MaxPayloads > next/s.b.config.MaxPayloads && s.requestedSet != next/s.b.config.MaxPayloads {
		s.requestedSet = next / s.b.config.MaxPayloads
		return s.send(qMissingSet(s.assembly.missingSet(s.b.config.MaxPayloads), s.b.config.MaxPayloads), false)
	}
	return nil
}

func (s *q2BodyReceiver[C]) acceptBodyType(response *pool.Message) error {
	mode := response.Type()
	if mode == message.Acknowledgement {
		mode = message.Confirmable
	}
	if mode != message.Confirmable && mode != message.NonConfirmable ||
		s.bodyTypeSet && mode != s.bodyType {
		return ErrInvalidOptionBlock2
	}
	s.bodyType, s.bodyTypeSet = mode, true
	return nil
}

func validQ2Response(response *pool.Message) bool {
	return !response.HasOption(message.Block1) && !response.HasOption(message.Block2) &&
		optionCount(response, message.QBlock2) == 1 &&
		optionCount(response, message.Size2) == 1 && optionCount(response, message.ETag) == 1
}

func (s *q2BodyReceiver[C]) accept(response *pool.Message) (bool, error) {
	if observe, err := s.req.Observe(); err == nil && observe == 1 && response.HasOption(message.Observe) {
		return false, nil
	}
	if !validQ2Response(response) {
		return false, ErrInvalidOptionBlock2
	}
	value, optionErr := response.GetOptionUint32(message.QBlock2)
	szx, num, more, err := DecodeQBlockOption(value)
	if optionErr != nil || err != nil || szx > s.maxSZX {
		return false, ErrInvalidOptionBlock2
	}
	skip, err := s.representation(response, szx)
	if err != nil || skip {
		return false, err
	}
	if err = s.acceptBodyType(response); err != nil {
		return false, err
	}
	complete, duplicate, err := s.add(response, num, more)
	if err != nil || complete || duplicate {
		return complete, err
	}
	return false, s.advance(num)
}

func (s *q2BodyReceiver[C]) finish(response *pool.Message) (*pool.Message, error) {
	body, err := s.assembly.ReadSeeker()
	if err != nil {
		s.b.cc.ReleaseMessage(response)
		return nil, err
	}
	response.Remove(message.QBlock2)
	response.Remove(message.Size2)
	response.SetBody(body)
	response.SetToken(s.req.Token())
	if s.observePresent {
		response.SetObserve(s.observeSequence)
	}
	return response, nil
}

// ReceiveQBlock2 consumes a Q body through an existing exchange, taking ownership
// of initial when supplied. Notification exchanges retain the observation token.
func (b *Engine[C]) ReceiveQBlock2(req *pool.Message, szx SZX, exchange Exchange, initial *pool.Message) (*pool.Message, error) {
	if !qReserveTransfer() {
		if initial != nil {
			b.cc.ReleaseMessage(initial)
		}
		return nil, ErrQBlockTooLarge
	}
	s := &q2BodyReceiver[C]{b: b, req: req, exchange: exchange, szx: szx, maxSZX: szx, requestedSet: -1}
	defer func() {
		qRetainedTransfers.Add(-1)
		qRetainedBytes.Add(-s.retainedBytes)
	}()
	if initial != nil {
		s.sentRequests = 1
	} else if err := s.send([]int64{0}, true); err != nil {
		return nil, err
	}
	for {
		response, receiveErr := initial, error(nil)
		initial = nil
		if response == nil {
			response, receiveErr = s.receive()
		}
		if receiveErr != nil {
			if err := s.recover(receiveErr); err != nil {
				return nil, err
			}
			continue
		}
		if !response.HasOption(message.QBlock2) {
			return s.unblocked(response)
		}
		complete, err := s.accept(response)
		if err != nil {
			b.cc.ReleaseMessage(response)
			return nil, err
		}
		if complete {
			return s.finish(response)
		}
		b.cc.ReleaseMessage(response)
	}
}

func (s *q2BodyReceiver[C]) unblocked(response *pool.Message) (*pool.Message, error) {
	if s.assembly == nil && response.Code() >= codes.BadRequest && response.Code() != codes.BadOption {
		response.SetToken(s.req.Token())
		return response, nil
	}
	s.b.cc.ReleaseMessage(response)
	if s.assembly == nil {
		return nil, ErrQBlockUnsupported
	}
	return nil, ErrInvalidOptionBlock2
}

func qMissingSet(missing []uint32, maxPayloads int64) []int64 {
	if len(missing) == 0 {
		return nil
	}
	set := int64(missing[0]) / maxPayloads
	var nums []int64
	for _, num := range missing {
		if int64(num)/maxPayloads != set {
			break
		}
		nums = append(nums, int64(num))
	}
	return nums
}
