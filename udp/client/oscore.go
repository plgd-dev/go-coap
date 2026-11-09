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
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

type udpOSCOREExchange struct {
	context               *oscore.Context
	exchange              oscore.Exchange
	observe               bool
	expires               time.Time
	hasObserveResponse    bool
	hasNotificationNumber bool
	notificationNumber    uint64
	cancelContext         *oscore.Context
	cancelExchange        oscore.Exchange
	cancelExpires         time.Time
}

type udpOSCORERequestContextKey struct{}

type udpOSCORERequestContext struct {
	context  *oscore.Context
	exchange oscore.Exchange
}

type udpOSCORERequest struct {
	digest  [sha256.Size]byte
	expires time.Time
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

func (cc *Conn) protectOutgoingRequest(req *pool.Message) (*pool.Message, func(), error) {
	if cc.oscoreProcessor == nil {
		return req, func() {}, nil
	}
	if req.Code() >= codes.Created && req.Code() < 192 {
		if err := cc.protectObservationResponse(req); err != nil {
			return nil, nil, err
		}
		return req, func() {}, nil
	}
	if req.Code() < codes.GET || req.Code() > codes.IPATCH {
		return req, func() {}, nil
	}
	original, err := udpPoolMessageToOSCOREMessage(req)
	if err != nil {
		return nil, nil, fmt.Errorf("read request for OSCORE: %w", err)
	}
	protected, context, exchange, err := cc.oscoreProcessor.ProtectRequestWithContext(original)
	if err != nil {
		return nil, nil, err
	}
	wireRequest := cc.AcquireMessage(req.Context())
	wireRequest.SetMessage(protected)
	key := string(req.Token())
	observeValue, observeErr := req.Options().Observe()
	isObserveRequest := observeErr == nil
	expires := time.Now().Add(ExchangeLifetime)
	cc.oscoreMu.Lock()
	next := udpOSCOREExchange{
		context:  context,
		exchange: exchange,
		observe:  isObserveRequest,
		expires:  expires,
	}
	if isObserveRequest && observeValue == 1 {
		if previous, ok := cc.oscoreExchanges[key]; ok && previous.observe {
			previous.cancelContext = context
			previous.cancelExchange = exchange
			previous.cancelExpires = time.Now().Add(ExchangeLifetime)
			next = previous
		}
	}
	cc.oscoreExchanges[key] = next
	cc.oscoreMu.Unlock()
	release := func() {
		cc.ReleaseMessage(wireRequest)
	}
	return wireRequest, release, nil
}

func (cc *Conn) unprotectIncomingResponse(resp *pool.Message) error {
	key := string(resp.Token())
	exchange, ok := cc.oscoreExchangeForResponse(key)
	if !ok {
		if !resp.HasOption(message.OSCORE) {
			return nil
		}
		return oscore.ErrNoSecurityContext
	}
	if !resp.HasOption(message.OSCORE) {
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
	resp.SetMessage(plaintext)
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
	if usedCancelExchange || !hasObserve && !qBlock2More {
		delete(cc.oscoreExchanges, key)
		return nil
	}
	cc.oscoreExchanges[key] = current
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
	if cc.oscoreProcessor == nil || req.Code() < codes.GET || req.Code() > codes.IPATCH {
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
		}))
		cc.oscoreMu.Lock()
		cc.oscoreRequests[req.MessageID()] = udpOSCORERequest{
			digest:  digest,
			expires: time.Now().Add(ExchangeLifetime),
		}
		cc.oscoreMu.Unlock()
	}
	req.SetMessage(plaintext)
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
	resp.SetMessage(protected)
	return cc.updateOSCOREObservationForResponse(req, resp, binding)
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
		delete(cc.oscoreObservations, key)
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
	resp.SetMessage(protected)
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
	resp.SetMessage(protected)
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
	for token, exchange := range cc.oscoreExchanges {
		if !exchange.expires.IsZero() && !now.Before(exchange.expires) {
			delete(cc.oscoreExchanges, token)
			continue
		}
		if exchange.cancelContext != nil && !exchange.cancelExpires.IsZero() &&
			!now.Before(exchange.cancelExpires) {
			exchange.cancelContext = nil
			exchange.cancelExchange = oscore.Exchange{}
			exchange.cancelExpires = time.Time{}
			cc.oscoreExchanges[token] = exchange
		}
	}
	for mid, request := range cc.oscoreRequests {
		if !now.Before(request.expires) {
			delete(cc.oscoreRequests, mid)
		}
	}
	cc.oscoreMu.Unlock()
}
