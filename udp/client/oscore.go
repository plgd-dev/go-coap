package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

type udpOSCOREExchange struct {
	context               *oscore.Context
	exchange              oscore.Exchange
	record                *udpOSCOREExchangeRecord
	observe               bool
	qBlock2               bool
	expires               time.Time
	hasObserveResponse    bool
	hasNotificationNumber bool
	notificationNumber    uint64
	cancelContext         *oscore.Context
	cancelExchange        oscore.Exchange
	cancelExpires         time.Time
}

type udpOSCOREExchangeRecord struct {
	exchange udpOSCOREExchange
	previous *udpOSCOREExchangeRecord
	state    udpOSCOREExchangeState
}

type udpOSCOREExchangeState uint8

const (
	udpOSCOREExchangePending udpOSCOREExchangeState = iota
	udpOSCOREExchangeSent
	udpOSCOREExchangeFailed

	maxOSCOREExchangeRecordsPerToken = 64
)

type udpOSCORERequestContextKey struct{}

type udpOSCORERequestContext struct {
	context  *oscore.Context
	exchange oscore.Exchange
	options  message.Options
}

type udpOSCORERequest struct {
	digest  [sha256.Size]byte
	expires time.Time
}

type udpOSCOREResponse struct {
	digest  [sha256.Size]byte
	expires time.Time
}

func cloneUDPOSCOREOptions(options message.Options) message.Options {
	cloned := make(message.Options, len(options))
	for i, option := range options {
		cloned[i] = message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)}
	}
	return cloned
}

func udpPoolMessageToOSCOREMessage(msg *pool.Message) (message.Message, error) {
	payload, err := msg.ReadBody()
	if err != nil {
		return message.Message{}, err
	}
	return message.Message{
		Code:      msg.Code(),
		Token:     msg.Token(),
		Options:   msg.Options(),
		Payload:   payload,
		Type:      msg.Type(),
		MessageID: msg.MessageID(),
	}, nil
}

func setMessagePreservingControlMessage(msg *pool.Message, replacement message.Message) {
	controlMessage := msg.ControlMessage()
	msg.SetMessage(replacement)
	msg.SetControlMessage(controlMessage)
}

type oscoreOuterBlockwiseContextKey struct{}

type oscoreResponseAlreadyProcessedContextKey struct{}

func isOSCOREResponseAlreadyProcessed(resp *pool.Message) bool {
	if resp.Context() == nil {
		return false
	}
	processed, _ := resp.Context().Value(oscoreResponseAlreadyProcessedContextKey{}).(bool)
	return processed
}

func (cc *Conn) protectOutgoingRequest(req *pool.Message) (*pool.Message, func(bool), error) {
	if cc.oscoreProcessor == nil {
		return req, nil, nil
	}
	if cc.isOuterBlockwiseContinuation(req) {
		return req, nil, nil
	}
	if req.Code() >= codes.Created && req.Code() < 192 {
		if err := cc.protectObservationResponse(req); err != nil {
			return nil, nil, err
		}
		return req, nil, nil
	}
	if req.Code() < codes.GET || req.Code() > codes.IPATCH {
		return req, nil, nil
	}
	return cc.protectRequestMessage(req)
}

func (cc *Conn) isOuterBlockwiseContinuation(req *pool.Message) bool {
	if req.Context() != nil {
		if outerBlockwise, _ := req.Context().Value(oscoreOuterBlockwiseContextKey{}).(bool); outerBlockwise {
			return true
		}
	}
	return cc.oscoreOuterBlockwise && req.HasOption(message.OSCORE) &&
		(req.HasOption(message.Block1) || req.HasOption(message.Block2)) &&
		cc.hasOSCOREExchange(req.Token())
}

func (cc *Conn) protectRequestMessage(req *pool.Message) (*pool.Message, func(bool), error) {
	original, err := udpPoolMessageToOSCOREMessage(req)
	if err != nil {
		return nil, nil, fmt.Errorf("read request for OSCORE: %w", err)
	}
	protected, context, exchange, err := cc.oscoreProcessor.ProtectRequestWithContext(original)
	if err != nil {
		return nil, nil, err
	}
	cleanupExchange, err := cc.trackOSCOREExchange(req, context, exchange)
	if err != nil {
		return nil, nil, err
	}
	wireRequest := cc.AcquireMessage(req.Context())
	wireRequest.SetMessage(protected)
	wireRequest.SetControlMessage(req.ControlMessage())
	finish := func(sent bool) {
		cleanupExchange(sent)
		cc.ReleaseMessage(wireRequest)
	}
	return wireRequest, finish, nil
}

func (cc *Conn) hasOSCOREExchange(token message.Token) bool {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	exchange, ok := cc.oscoreExchanges[string(token)]
	return ok && (exchange.expires.IsZero() || time.Now().Before(exchange.expires))
}

func (cc *Conn) requiresOSCOREResponse(token message.Token) bool {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	if _, ok := cc.oscoreRequiredResponses[string(token)]; ok {
		return true
	}
	exchange, ok := cc.oscoreExchanges[string(token)]
	return ok && (exchange.expires.IsZero() || time.Now().Before(exchange.expires))
}

func (cc *Conn) trackOSCOREExchange(
	req *pool.Message, context *oscore.Context, exchange oscore.Exchange,
) (func(bool), error) {
	key := string(req.Token())
	observeValue, observeErr := req.Options().Observe()
	isObserveRequest := observeErr == nil
	expires := time.Now().Add(ExchangeLifetime)
	cc.oscoreMu.Lock()
	previous, hadPrevious := cc.oscoreExchanges[key]
	var previousRecord *udpOSCOREExchangeRecord
	if hadPrevious {
		previousRecord = previous.record
		if previousRecord == nil {
			previousRecord = &udpOSCOREExchangeRecord{
				exchange: previous,
				state:    udpOSCOREExchangeSent,
			}
		}
	}
	previousRecord, previousCount := pruneOSCOREExchangeRecords(previousRecord, time.Now())
	hadPrevious = previousRecord != nil
	if previousCount >= maxOSCOREExchangeRecordsPerToken {
		cc.oscoreMu.Unlock()
		return nil, fmt.Errorf(
			"too many pending OSCORE exchanges for token: limit %d",
			maxOSCOREExchangeRecordsPerToken,
		)
	}
	if hadPrevious {
		previous = previousRecord.exchange
	}
	next := udpOSCOREExchange{
		context:  context,
		exchange: exchange,
		observe:  isObserveRequest,
		qBlock2:  req.HasOption(message.QBlock2),
		expires:  expires,
	}
	if isObserveRequest && observeValue == 1 && hadPrevious && previous.observe {
		next = previous
		next.cancelContext = context
		next.cancelExchange = exchange
		next.cancelExpires = expires
	}
	record := &udpOSCOREExchangeRecord{
		exchange: next,
		previous: previousRecord,
		state:    udpOSCOREExchangePending,
	}
	next.record = record
	record.exchange = next
	cc.oscoreExchanges[key] = next
	if cc.oscoreRequiredResponses == nil {
		cc.oscoreRequiredResponses = make(map[string]struct{})
	}
	cc.oscoreRequiredResponses[key] = struct{}{}
	cc.oscoreMu.Unlock()
	return func(sent bool) {
		cc.finishOSCOREExchange(key, record, sent)
	}, nil
}

func pruneOSCOREExchangeRecords(
	record *udpOSCOREExchangeRecord,
	now time.Time,
) (*udpOSCOREExchangeRecord, int) {
	var head, tail *udpOSCOREExchangeRecord
	count := 0
	for current := record; current != nil; {
		next := current.previous
		expired := !current.exchange.expires.IsZero() && !now.Before(current.exchange.expires)
		if current.state != udpOSCOREExchangeFailed && !expired {
			if head == nil {
				head = current
			} else {
				tail.previous = current
			}
			tail = current
			count++
		}
		current = next
	}
	if tail != nil {
		tail.previous = nil
	}
	return head, count
}

func (cc *Conn) finishOSCOREExchange(key string, record *udpOSCOREExchangeRecord, sent bool) {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	if sent {
		if record.state == udpOSCOREExchangePending {
			record.state = udpOSCOREExchangeSent
			record.previous = nil
		}
		return
	}
	record.state = udpOSCOREExchangeFailed
	current, exists := cc.oscoreExchanges[key]
	if !exists || current.record != record {
		return
	}
	previous := record.previous
	now := time.Now()
	for previous != nil && (previous.state == udpOSCOREExchangeFailed ||
		!previous.exchange.expires.IsZero() && now.After(previous.exchange.expires)) {
		previous = previous.previous
	}
	if previous == nil {
		delete(cc.oscoreExchanges, key)
		return
	}
	cc.oscoreExchanges[key] = previous.exchange
}

func (cc *Conn) unprotectIncomingResponse(resp *pool.Message) error {
	key := string(resp.Token())
	exchange, ok := cc.oscoreExchangeForResponse(key)
	if !ok {
		if !resp.HasOption(message.OSCORE) {
			if cc.requiresOSCOREResponse(resp.Token()) {
				return oscore.ErrOSCORERequired
			}
			return nil
		}
		return oscore.ErrNoSecurityContext
	}
	if !resp.HasOption(message.OSCORE) {
		if oscore.IsUnprotectedProcessingErrorCode(resp.Code()) {
			cc.oscoreMu.Lock()
			if current, exists := cc.oscoreExchanges[key]; exists && current.record == exchange.record {
				delete(cc.oscoreExchanges, key)
			}
			delete(cc.oscoreRequiredResponses, key)
			cc.oscoreMu.Unlock()
			resp.SetContext(oscore.ContextWithUnprotectedProcessingError(resp.Context()))
			return nil
		}
		return oscore.ErrOSCORERequired
	}
	outer, err := udpPoolMessageToOSCOREMessage(resp)
	if err != nil {
		return fmt.Errorf("read OSCORE response: %w", err)
	}
	plaintext, usedCancelExchange, err := cc.unprotectResponseWithExchange(outer, exchange)
	if err != nil {
		return err
	}
	responseOption, err := oscore.UnmarshalOption(findOSCOREOption(resp.Options()))
	if err != nil {
		return fmt.Errorf("decode OSCORE response option: %w", err)
	}
	hasObserve := plaintext.Options.HasOption(message.Observe)
	if err := cc.updateOSCOREExchangeResponse(key, exchange, responseOption, usedCancelExchange, hasObserve, plaintext.Options); err != nil {
		return err
	}
	if hasObserve && len(responseOption.PartialIV) > 0 {
		observation.SetAuthenticatedSequenceNumber(resp, partialIVNumber(responseOption.PartialIV))
	}
	setMessagePreservingControlMessage(resp, plaintext)
	resp.SetContext(context.WithValue(resp.Context(), oscoreResponseAlreadyProcessedContextKey{}, true))
	return nil
}

func (cc *Conn) oscoreExchangeForResponse(key string) (udpOSCOREExchange, bool) {
	cc.oscoreMu.Lock()
	exchange, ok := cc.oscoreExchanges[key]
	if ok && !exchange.expires.IsZero() && time.Now().After(exchange.expires) {
		delete(cc.oscoreExchanges, key)
		ok = false
	}
	cc.oscoreMu.Unlock()
	return exchange, ok
}

func (cc *Conn) unprotectResponseWithExchange(outer message.Message, exchange udpOSCOREExchange) (message.Message, bool, error) {
	plaintext, err := cc.oscoreProcessor.UnprotectResponse(outer, exchange.context, exchange.exchange)
	usedCancelExchange := false
	if err != nil && exchange.cancelContext != nil &&
		(exchange.cancelExpires.IsZero() || time.Now().Before(exchange.cancelExpires)) {
		plaintext, err = cc.oscoreProcessor.UnprotectResponse(outer, exchange.cancelContext, exchange.cancelExchange)
		usedCancelExchange = err == nil
	}
	if err != nil {
		return message.Message{}, false, err
	}
	return plaintext, usedCancelExchange, nil
}

func (cc *Conn) updateOSCOREExchangeResponse(
	key string,
	exchange udpOSCOREExchange,
	responseOption oscore.OptionValue,
	usedCancelExchange bool,
	hasObserve bool,
	options message.Options,
) error {
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	current, exists := cc.oscoreExchanges[key]
	if !exists || !bytes.Equal(current.exchange.RequestPartialIV, exchange.exchange.RequestPartialIV) {
		return nil
	}
	if err := updateOSCOREObserveResponse(&current, responseOption, usedCancelExchange, hasObserve); err != nil {
		return err
	}
	qBlock2More := !usedCancelExchange && !hasObserve && qBlockResponseHasMore(options)
	if usedCancelExchange || !hasObserve && !current.qBlock2 && !qBlock2More {
		delete(cc.oscoreExchanges, key)
		delete(cc.oscoreRequiredResponses, key)
		return nil
	}
	cc.oscoreExchanges[key] = current
	if current.record != nil {
		current.record.exchange = current
	}
	return nil
}

func updateOSCOREObserveResponse(
	current *udpOSCOREExchange,
	responseOption oscore.OptionValue,
	usedCancelExchange bool,
	hasObserve bool,
) error {
	if !current.observe || usedCancelExchange {
		return nil
	}
	if current.hasObserveResponse && len(responseOption.PartialIV) == 0 {
		return oscore.ErrReplay
	}
	if len(responseOption.PartialIV) > 0 {
		notificationNumber := partialIVNumber(responseOption.PartialIV)
		if current.hasNotificationNumber && notificationNumber <= current.notificationNumber {
			return oscore.ErrReplay
		}
		current.hasNotificationNumber = true
		current.notificationNumber = notificationNumber
	}
	current.hasObserveResponse = true
	if hasObserve {
		current.expires = time.Time{}
	}
	return nil
}

func qBlockResponseHasMore(options message.Options) bool {
	for _, option := range options {
		if option.ID != message.QBlock2 {
			continue
		}
		value, _, err := message.DecodeUint32(option.Value)
		if err != nil {
			return false
		}
		_, _, more, err := blockwise.DecodeQBlockOption(value)
		if err != nil {
			return false
		}
		if more {
			return true
		}
	}
	return false
}

func (cc *Conn) processIncomingOSCORERequest(req *pool.Message, datagram []byte) (bool, error) {
	if cc.oscoreProcessor == nil ||
		!req.HasOption(message.OSCORE) && (req.Code() < codes.GET || req.Code() > codes.IPATCH) {
		return false, nil
	}
	digest := sha256.Sum256(datagram)
	cc.oscoreMu.Lock()
	cached, exists := cc.oscoreRequests[req.MessageID()]
	if exists && time.Now().After(cached.expires) {
		delete(cc.oscoreRequests, req.MessageID())
		exists = false
	}
	cc.oscoreMu.Unlock()
	if exists {
		if cached.digest != digest {
			return false, fmt.Errorf("%w: Message ID reused for different protected request", oscore.ErrInvalidOSCOREMessage)
		}
		return true, cc.retransmitOSCOREResponse(req)
	}

	outer, err := udpPoolMessageToOSCOREMessage(req)
	if err != nil {
		return false, fmt.Errorf("read incoming request: %w", err)
	}
	plaintext, securityContext, exchange, err := cc.oscoreProcessor.ProcessIncomingRequest(outer)
	if err != nil {
		return false, err
	}
	if securityContext != nil {
		req.SetContext(context.WithValue(req.Context(), udpOSCORERequestContextKey{}, udpOSCORERequestContext{
			context:  securityContext,
			exchange: exchange,
			options:  cloneUDPOSCOREOptions(plaintext.Options),
		}))
		cc.oscoreMu.Lock()
		cc.oscoreRequests[req.MessageID()] = udpOSCORERequest{
			digest:  digest,
			expires: time.Now().Add(ExchangeLifetime),
		}
		cc.oscoreMu.Unlock()
	}
	setMessagePreservingControlMessage(req, plaintext)
	return false, nil
}

func (cc *Conn) retransmitOSCOREResponse(req *pool.Message) error {
	resp := cc.AcquireMessage(req.Context())
	defer cc.ReleaseMessage(resp)
	ok, err := cc.getResponseFromCache(req.MessageID(), resp)
	if err != nil {
		return fmt.Errorf("load cached OSCORE response: %w", err)
	}
	if !ok {
		return nil
	}
	resp.SetMessageID(req.MessageID())
	if req.Type() == message.Confirmable {
		resp.SetType(message.Acknowledgement)
	} else {
		resp.SetType(message.NonConfirmable)
	}
	return cc.session.WriteMessage(resp)
}

func (cc *Conn) protectResponseForRequest(req, resp *pool.Message) error {
	if cc.oscoreProcessor == nil || req.Context() == nil {
		return nil
	}
	if resp.HasOption(message.OSCORE) {
		return nil
	}
	binding, ok := req.Context().Value(udpOSCORERequestContextKey{}).(udpOSCORERequestContext)
	if !ok || resp.Code() < codes.Created || resp.Code() >= 192 {
		return nil
	}
	response, err := udpPoolMessageToOSCOREMessage(resp)
	if err != nil {
		return fmt.Errorf("read response for OSCORE: %w", err)
	}
	protected, err := protectOSCOREResponse(binding, response)
	if err != nil {
		return fmt.Errorf("protect OSCORE response: %w", err)
	}
	setMessagePreservingControlMessage(resp, protected)
	return cc.updateOSCOREObservationForResponse(req, resp, binding)
}

func (cc *Conn) oscoreResponseExceedsOuterBlockwiseLimit(req, resp *pool.Message) (bool, error) {
	limit := int64(cc.oscoreOuterBlockwiseMaxReassembledPayloadSize)
	if limit <= 0 {
		return false, nil
	}
	if req.Context() == nil {
		return false, nil
	}
	binding, ok := req.Context().Value(udpOSCORERequestContextKey{}).(udpOSCORERequestContext)
	if !ok {
		return false, nil
	}
	response, err := udpPoolMessageToOSCOREMessage(resp)
	if err != nil {
		return false, fmt.Errorf("read response: %w", err)
	}
	split, err := oscore.SplitOptions(response.Options)
	if err != nil {
		return false, fmt.Errorf("split response options: %w", err)
	}
	for i := range split.Inner {
		if split.Inner[i].ID == message.Observe {
			split.Inner[i].Value = nil
		}
	}
	plaintext, err := oscore.EncodeInner(response.Code, split.Inner, response.Payload)
	if err != nil {
		return false, fmt.Errorf("encode response inner message: %w", err)
	}
	var tagLength int
	switch binding.context.Algorithm() {
	case oscore.AEADAlgorithmAESCCM16_64_128, oscore.AEADAlgorithmAESCCM16_64_256:
		tagLength = 8
	case oscore.AEADAlgorithmAESCCM16_128_128, oscore.AEADAlgorithmAESCCM16_128_256:
		tagLength = 16
	default:
		return false, fmt.Errorf("unsupported OSCORE AEAD algorithm %d", binding.context.Algorithm())
	}
	return int64(len(plaintext)+tagLength) > limit, nil
}

func protectOSCOREResponse(binding udpOSCORERequestContext, response message.Message) (message.Message, error) {
	if binding.exchange.Observe || response.Options.HasOption(message.Observe) ||
		response.Options.HasOption(message.QBlock1) || response.Options.HasOption(message.QBlock2) {
		return binding.context.ProtectResponseWithPartialIV(response, binding.exchange)
	}
	return binding.context.ProtectResponse(response, binding.exchange)
}

func (cc *Conn) updateOSCOREObservationForResponse(
	req, resp *pool.Message,
	binding udpOSCORERequestContext,
) error {
	observeValue, observeErr := req.Observe()
	key := string(req.Token())
	cc.oscoreMu.Lock()
	defer cc.oscoreMu.Unlock()
	switch {
	case observeErr == nil && observeValue == 1:
		if current, exists := cc.oscoreObservations[key]; exists &&
			oscore.MatchesObserveRegistration(current.options, binding.options) {
			delete(cc.oscoreObservations, key)
		}
	case observeErr == nil && observeValue == 0 &&
		resp.Code() >= codes.Created && resp.Code() < codes.BadRequest && resp.HasOption(message.Observe):
		cc.oscoreObservations[key] = binding
	case binding.exchange.Observe && !resp.HasOption(message.Observe):
		delete(cc.oscoreObservations, key)
	}
	return nil
}

func (cc *Conn) protectObservationResponse(resp *pool.Message) error {
	if cc.oscoreProcessor == nil || resp.HasOption(message.OSCORE) ||
		resp.Code() < codes.Created || resp.Code() >= 192 {
		return nil
	}
	key := string(resp.Token())
	if handled, err := cc.protectContextQBlockResponse(resp); handled {
		return err
	}
	cc.oscoreMu.Lock()
	binding, ok := cc.oscoreObservations[key]
	cc.oscoreMu.Unlock()
	if !ok {
		return nil
	}
	response, err := udpPoolMessageToOSCOREMessage(resp)
	if err != nil {
		return fmt.Errorf("read Observe notification for OSCORE: %w", err)
	}
	protected, err := binding.context.ProtectResponseWithPartialIV(response, binding.exchange)
	if err != nil {
		return fmt.Errorf("protect OSCORE Observe notification: %w", err)
	}
	setMessagePreservingControlMessage(resp, protected)
	if !resp.HasOption(message.Observe) {
		cc.oscoreMu.Lock()
		delete(cc.oscoreObservations, key)
		cc.oscoreMu.Unlock()
	}
	return nil
}

func (cc *Conn) protectContextQBlockResponse(resp *pool.Message) (bool, error) {
	if resp.Context() == nil {
		return false, nil
	}
	binding, ok := resp.Context().Value(udpOSCORERequestContextKey{}).(udpOSCORERequestContext)
	if !ok || !resp.HasOption(message.QBlock1) && !resp.HasOption(message.QBlock2) {
		return false, nil
	}
	response, err := udpPoolMessageToOSCOREMessage(resp)
	if err != nil {
		return true, fmt.Errorf("read Q-Block response for OSCORE: %w", err)
	}
	protected, err := binding.context.ProtectResponseWithPartialIV(response, binding.exchange)
	if err != nil {
		return true, fmt.Errorf("protect Q-Block response: %w", err)
	}
	setMessagePreservingControlMessage(resp, protected)
	return true, nil
}

func findOSCOREOption(options message.Options) []byte {
	start, end, err := options.Find(message.OSCORE)
	if err != nil || end-start != 1 {
		return nil
	}
	return options[start].Value
}

func partialIVNumber(partialIV []byte) uint64 {
	var number uint64
	for _, b := range partialIV {
		number = number<<8 | uint64(b)
	}
	return number
}

func (cc *Conn) expireOSCORERequests(now time.Time) {
	cc.oscoreMu.Lock()
	cc.expireOSCOREExchanges(now)
	cc.expireOSCORERequiredResponses()
	cc.expireOSCORERequestCache(now)
	cc.expireOSCOREResponseCache(now)
	cc.oscoreMu.Unlock()
}

func (cc *Conn) expireOSCOREExchanges(now time.Time) {
	for token, exchange := range cc.oscoreExchanges {
		if !exchange.expires.IsZero() && !now.Before(exchange.expires) {
			delete(cc.oscoreExchanges, token)
		}
		if exchange.cancelContext != nil && !exchange.cancelExpires.IsZero() &&
			!now.Before(exchange.cancelExpires) {
			exchange.cancelContext = nil
			exchange.cancelExchange = oscore.Exchange{}
			exchange.cancelExpires = time.Time{}
			cc.oscoreExchanges[token] = exchange
		}
	}
}

func (cc *Conn) expireOSCORERequiredResponses() {
	for token := range cc.oscoreRequiredResponses {
		if _, hasExchange := cc.oscoreExchanges[token]; hasExchange {
			continue
		}
		if !cc.hasOSCOREResponseConsumer(token) {
			delete(cc.oscoreRequiredResponses, token)
		}
	}
}

func (cc *Conn) hasOSCOREResponseConsumer(token string) bool {
	_, hasHandler := cc.tokenHandlerContainer.Load(message.Token(token).Hash())
	_, hasObservation := cc.oscoreObservations[token]
	return hasHandler || hasObservation
}

func (cc *Conn) expireOSCORERequestCache(now time.Time) {
	for mid, request := range cc.oscoreRequests {
		if !now.Before(request.expires) {
			delete(cc.oscoreRequests, mid)
		}
	}
}

func (cc *Conn) expireOSCOREResponseCache(now time.Time) {
	for mid, response := range cc.oscoreResponses {
		if !now.Before(response.expires) {
			delete(cc.oscoreResponses, mid)
		}
	}
}
