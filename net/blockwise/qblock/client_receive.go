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
	pkgMath "github.com/plgd-dev/go-coap/v3/pkg/math"
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
	b                        *Engine[C]
	req                      *pool.Message
	exchange                 Exchange
	szx, maxSZX              SZX
	assembly                 *Assembly
	etag                     []byte
	continued                int64
	partialExpires           time.Time
	retries                  int
	requestedSet             int64
	retainedBytes            int64
	sentRequests             int
	observeSequence          uint32
	observePresent           bool
	observeTime              time.Time
	authenticatedSequence    uint64
	hasAuthenticatedSequence bool
	bodyType                 message.Type
	bodyTypeSet              bool
	terminal                 bool
	representationCode       codes.Code
}

func (s *q2BodyReceiver[C]) send(nums []int64, more bool) error {
	for offset := 0; offset < len(nums); {
		part, added, err := s.buildRequestPart(nums[offset:], more)
		if err != nil {
			return err
		}
		offset += added
		s.sentRequests++
		err = s.exchange.Send(part)
		s.b.cc.ReleaseMessage(part)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *q2BodyReceiver[C]) buildRequestPart(nums []int64, more bool) (*pool.Message, int, error) {
	part, err := s.newRequest()
	if err != nil {
		return nil, 0, err
	}
	if budget, ok := any(s.b.cc).(interface {
		QBlockDatagramBudget(*pool.Message) (baseSize, perOptionOverhead, limit uint64, err error)
	}); ok {
		added, err := s.addQBlockOptionsWithinBudget(part, nums, more, budget)
		if err != nil {
			s.b.cc.ReleaseMessage(part)
			return nil, 0, err
		}
		if added == 0 {
			s.b.cc.ReleaseMessage(part)
			return nil, 0, ErrQBlockTooLarge
		}
		if err := addQBlockOptions(part, s.szx, nums[:added], more); err != nil {
			s.b.cc.ReleaseMessage(part)
			return nil, 0, err
		}
		if err := validateQBlockDatagram(s.b.cc, part); err != nil {
			s.b.cc.ReleaseMessage(part)
			return nil, 0, err
		}
		return part, added, nil
	}

	added := 0
	for _, num := range nums {
		value, err := EncodeQBlockOption(s.szx, num, more)
		if err != nil {
			s.b.cc.ReleaseMessage(part)
			return nil, 0, err
		}
		part.AddOptionUint32(message.QBlock2, value)
		if err = validateQBlockDatagram(s.b.cc, part); err != nil {
			part.Remove(message.QBlock2)
			if added == 0 {
				s.b.cc.ReleaseMessage(part)
				return nil, 0, err
			}
			if err := addQBlockOptions(part, s.szx, nums[:added], more); err != nil {
				s.b.cc.ReleaseMessage(part)
				return nil, 0, err
			}
			break
		}
		added++
	}
	if added == 0 {
		s.b.cc.ReleaseMessage(part)
		return nil, 0, ErrQBlockTooLarge
	}
	return part, added, nil
}

func (s *q2BodyReceiver[C]) addQBlockOptionsWithinBudget(
	part *pool.Message,
	nums []int64,
	more bool,
	budget interface {
		QBlockDatagramBudget(*pool.Message) (baseSize, perOptionOverhead, limit uint64, err error)
	},
) (int, error) {
	baseSize, perOptionOverhead, limit, err := budget.QBlockDatagramBudget(part)
	if err != nil {
		return 0, err
	}
	tailAdjustment, previousID, err := qBlockOptionTailAdjustment(part.Options())
	if err != nil {
		return 0, err
	}
	baseSize, err = applyQBlockTailAdjustment(baseSize, tailAdjustment)
	if err != nil {
		return 0, err
	}
	if baseSize > limit {
		return 0, nil
	}
	return s.countFittingQBlockOptions(nums, more, baseSize, perOptionOverhead, limit, previousID)
}

func applyQBlockTailAdjustment(baseSize uint64, tailAdjustment int64) (uint64, error) {
	if tailAdjustment < 0 {
		decrease, err := pkgMath.SafeCastTo[uint64](-(tailAdjustment + 1))
		if err != nil {
			return 0, err
		}
		decrease++
		if decrease > baseSize {
			return 0, errors.New("invalid Q-Block datagram size estimate")
		}
		return baseSize - decrease, nil
	}
	increase, err := pkgMath.SafeCastTo[uint64](tailAdjustment)
	if err != nil {
		return 0, err
	}
	if increase > ^uint64(0)-baseSize {
		return 0, errors.New("Q-Block datagram size estimate overflow")
	}
	return baseSize + increase, nil
}

func (s *q2BodyReceiver[C]) countFittingQBlockOptions(
	nums []int64,
	more bool,
	baseSize, perOptionOverhead, limit uint64,
	previousID message.OptionID,
) (int, error) {
	size := baseSize
	added := 0
	for _, num := range nums {
		value, err := EncodeQBlockOption(s.szx, num, more)
		if err != nil {
			return 0, err
		}
		option, err := newQBlockMessageOption(value)
		if err != nil {
			return 0, err
		}
		increment, err := encodedOptionSize(option, previousID)
		if err != nil {
			return 0, err
		}
		incrementSize, err := pkgMath.SafeCastTo[uint64](increment)
		if err != nil {
			return 0, err
		}
		if size > limit || incrementSize > limit-size ||
			perOptionOverhead > limit-size-incrementSize {
			break
		}
		size += incrementSize + perOptionOverhead
		added++
		previousID = message.QBlock2
	}
	return added, nil
}

func qBlockOptionTailAdjustment(options message.Options) (int64, message.OptionID, error) {
	var previousID message.OptionID
	for _, option := range options {
		switch {
		case option.ID < message.QBlock2:
			previousID = option.ID
		case option.ID == message.QBlock2:
			return 0, 0, errors.New("Q-Block2 option already present in request part")
		default:
			originalSize, err := encodedOptionSize(option, previousID)
			if err != nil {
				return 0, 0, err
			}
			insertedSize, err := encodedOptionSize(option, message.QBlock2)
			if err != nil {
				return 0, 0, err
			}
			return int64(insertedSize - originalSize), previousID, nil
		}
	}
	return 0, previousID, nil
}

func newQBlockMessageOption(value uint32) (message.Option, error) {
	var encoded [4]byte
	length, err := message.EncodeUint32(encoded[:], value)
	if err != nil {
		return message.Option{}, err
	}
	return message.Option{ID: message.QBlock2, Value: encoded[:length]}, nil
}

func encodedOptionSize(option message.Option, previousID message.OptionID) (int, error) {
	size, err := option.Marshal(nil, previousID)
	if err != nil && !errors.Is(err, message.ErrTooSmall) {
		return 0, err
	}
	return size, nil
}

func (s *q2BodyReceiver[C]) newRequest() (*pool.Message, error) {
	part := s.b.cloneMessage(s.req)
	part.SetMessageID(0)
	part.Remove(message.ETag)
	part.Remove(message.QBlock1)
	part.Remove(message.QBlock2)
	part.SetBody(nil)
	if s.sentRequests > 0 {
		token, err := s.exchange.NewToken()
		if err != nil {
			s.b.cc.ReleaseMessage(part)
			return nil, err
		}
		part.SetToken(token)
		part.Remove(message.Observe)
	}
	return part, nil
}

func validateQBlockDatagram(client any, part *pool.Message) error {
	validator, ok := client.(interface {
		ValidateQBlockDatagram(*pool.Message) error
	})
	if !ok {
		return nil
	}
	return validator.ValidateQBlockDatagram(part)
}

func addQBlockOptions(part *pool.Message, szx SZX, nums []int64, more bool) error {
	for _, num := range nums {
		value, err := EncodeQBlockOption(szx, num, more)
		if err != nil {
			return err
		}
		part.AddOptionUint32(message.QBlock2, value)
	}
	return nil
}

func (s *q2BodyReceiver[C]) receive() (*pool.Message, error) {
	deadline := time.Now().Add(s.b.config.NonReceiveTimeout << s.retries)
	if !s.partialExpires.IsZero() && s.partialExpires.Before(deadline) {
		deadline = s.partialExpires
	}
	return receiveQBlockResponse(s.req.Context(), s.exchange, deadline)
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
	s.observePresent = false
	s.representationCode = response.Code()
	if sequence, sequenceErr := response.Observe(); sequenceErr == nil {
		s.observeSequence, s.observePresent, s.observeTime = sequence, true, time.Now()
	}
	if sequence, ok := observation.AuthenticatedSequenceNumber(response.Context()); ok {
		s.authenticatedSequence, s.hasAuthenticatedSequence = sequence, true
	}
	maxAge, maxAgeErr := response.GetOptionUint32(message.MaxAge)
	if maxAgeErr != nil {
		maxAge = 60
	}
	partialLifetime := time.Duration(maxAge) * time.Second
	if maxAge == 0 {
		partialLifetime = s.b.config.NonPartialTimeout()
	}
	s.partialExpires = time.Now().Add(min(s.b.config.NonPartialTimeout(), partialLifetime))
	return nil
}

// representation checks identity before retaining bytes; older representations
// can still arrive on retained recovery tokens after a newer notification starts.
func (s *q2BodyReceiver[C]) representation(response *pool.Message, szx SZX) (bool, error) {
	size, etag, err := q2RepresentationIdentity(response)
	if err != nil {
		return false, err
	}
	sequence, sequenceErr := response.Observe()
	changed := s.representationChanged(response.Code(), size, etag)
	if s.skipAfterTerminal(response, changed) {
		return true, nil
	}
	terminal := s.registrationTerminal(response)
	if changed && s.observePresent {
		newer := s.isNewerSequence(response, sequence, sequenceErr)
		if !newer && !terminal {
			return true, nil
		}
		s.b.qReleaseBytes(s.retainedBytes)
		s.retainedBytes, s.assembly = 0, nil
		s.continued, s.retries, s.requestedSet = 0, 0, -1
	}
	if s.assembly == nil {
		s.terminal = terminal
		return false, s.newRepresentation(response, szx, size, etag)
	}
	if changed || szx != s.szx {
		return false, ErrQBlockConflict
	}
	if terminal {
		s.terminal, s.observePresent = true, false
	}
	if s.isNewerSequence(response, sequence, sequenceErr) {
		s.observeSequence, s.observeTime = sequence, time.Now()
		if authenticatedSequence, ok := observation.AuthenticatedSequenceNumber(response.Context()); ok {
			s.authenticatedSequence, s.hasAuthenticatedSequence = authenticatedSequence, true
		}
	}
	return false, nil
}

func q2RepresentationIdentity(response *pool.Message) (uint32, []byte, error) {
	size, sizeErr := response.GetOptionUint32(message.Size2)
	etag, etagErr := response.GetOptionBytes(message.ETag)
	if sizeErr != nil || etagErr != nil || len(etag) == 0 || len(etag) > 8 {
		return 0, nil, ErrInvalidOptionBlock2
	}
	return size, etag, nil
}

func (s *q2BodyReceiver[C]) representationChanged(code codes.Code, size uint32, etag []byte) bool {
	return s.assembly != nil && (!bytes.Equal(s.etag, etag) ||
		int64(size) != s.assembly.bodySize || code != s.representationCode)
}

func (s *q2BodyReceiver[C]) skipAfterTerminal(response *pool.Message, changed bool) bool {
	return s.terminal && (response.HasOption(message.Observe) || changed && !s.registrationTerminal(response))
}

func (s *q2BodyReceiver[C]) isNewerSequence(response *pool.Message, sequence uint32, err error) bool {
	if authenticatedSequence, ok := observation.AuthenticatedSequenceNumber(response.Context()); ok {
		return !s.hasAuthenticatedSequence || authenticatedSequence > s.authenticatedSequence
	}
	return s.observePresent && err == nil && sequence != s.observeSequence &&
		observation.ValidSequenceNumber(s.observeSequence, sequence, s.observeTime, time.Now())
}

func (s *q2BodyReceiver[C]) add(response *pool.Message, num int64, more bool) (bool, bool, error) {
	payload, err := readQBody(response)
	if err != nil {
		return false, false, err
	}
	if !s.b.qReserveBytes(int64(len(payload))) {
		return false, false, ErrQBlockTooLarge
	}
	complete, duplicate, err := s.assembly.Add(num, more, payload)
	if duplicate || err != nil {
		s.b.qReleaseBytes(int64(len(payload)))
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
	s.req.SetType(mode)
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
	if !b.qReserveTransfer() {
		if initial != nil {
			b.cc.ReleaseMessage(initial)
		}
		return nil, ErrQBlockTooLarge
	}
	s := &q2BodyReceiver[C]{b: b, req: req, exchange: exchange, szx: szx, maxSZX: szx, requestedSet: -1}
	defer func() {
		b.qReleaseTransfer()
		b.qReleaseBytes(s.retainedBytes)
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
	if s.registrationTerminal(response) ||
		response.Code() >= codes.BadRequest && response.Code() != codes.BadOption {
		response.SetToken(s.req.Token())
		return response, nil
	}

	s.b.cc.ReleaseMessage(response)
	if s.assembly == nil {
		return nil, ErrQBlockUnsupported
	}
	return nil, ErrInvalidOptionBlock2
}

func (s *q2BodyReceiver[C]) registrationTerminal(response *pool.Message) bool {
	observe, err := s.req.Observe()
	return err == nil && observe == 0 && !response.HasOption(message.Observe) &&
		bytes.Equal(response.Token(), s.req.Token()) && response.Code() >= codes.Created &&
		q2StartsRepresentation(response)
}

func q2StartsRepresentation(response *pool.Message) bool {
	if !response.HasOption(message.QBlock2) {
		return true
	}
	value, err := response.GetOptionUint32(message.QBlock2)
	if err != nil {
		return false
	}
	_, num, _, err := DecodeQBlockOption(value)
	return err == nil && num == 0
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
