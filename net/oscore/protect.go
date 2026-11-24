package oscore

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

var (
	ErrInvalidOSCOREMessage = errors.New("invalid OSCORE message")
	ErrInvalidOSCOREOption  = errors.New("invalid OSCORE option")
	ErrUnsupportedOption    = errors.New("unsupported OSCORE option processing")
	ErrReplayTrackingLimit  = errors.New("OSCORE response replay tracking limit exceeded")
)

const outerResponseCode = codes.Changed

const (
	maxQBlock2ObserveWindowSize     uint64 = 1 << 20
	defaultQBlock2ObserveWindowSize        = uint64((1 << 16) * 5)
)

// Exchange binds a response to the request that established its OSCORE AAD
// and nonce context. Keep the exact value returned by ProtectRequest or
// UnprotectRequest with the CoAP token until the exchange completes; response
// replay state is shared by copies of this value.
type Exchange struct {
	responseState *exchangeResponseState
}

type exchangeResponseState struct {
	mu                        sync.Mutex
	requestToken              message.Token
	hasRequestToken           bool
	requestID                 []byte
	requestPartialIV          []byte
	observe                   bool
	qBlock2Observe            bool
	context                   *Context
	started                   bool
	observeComplete           bool
	multipleResponses         bool
	responseProtected         bool
	hasResponseSequence       bool
	lastResponseSequence      uint64
	hasCompletedQBlock2       bool
	completedQBlock2HighWater uint64
	seenResponsePartialIVs    map[uint64]struct{}
}

// PartialIVNumber converts a big-endian Partial IV into its sequence number.
func PartialIVNumber(partialIV []byte) uint64 {
	var number uint64
	for _, b := range partialIV {
		number = number<<8 | uint64(b)
	}
	return number
}

// RequestIdentifier returns a copy of the request identifier bound to the
// Exchange.
func (e Exchange) RequestIdentifier() []byte {
	if e.responseState == nil {
		return nil
	}
	return append([]byte(nil), e.responseState.requestID...)
}

// RequestPartialIVBytes returns a copy of the request Partial IV bound to the
// Exchange.
func (e Exchange) RequestPartialIVBytes() []byte {
	if e.responseState == nil {
		return nil
	}
	return append([]byte(nil), e.responseState.requestPartialIV...)
}

// IsObserve reports whether the Exchange is bound to an Observe registration.
func (e Exchange) IsObserve() bool {
	return e.responseState != nil && e.responseState.observe
}

// CompleteQBlock2Representation retires per-fragment replay entries after the
// caller has successfully assembled a protected Q-Block2 Observe representation.
func (e Exchange) CompleteQBlock2Representation(highestPartialIV uint64) error {
	state := e.responseState
	if state == nil {
		return ErrInvalidContext
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.qBlock2Observe {
		return fmt.Errorf("%w: exchange is not a Q-Block2 Observe exchange", ErrInvalidContext)
	}
	if !state.hasResponseSequence || highestPartialIV > state.lastResponseSequence {
		return fmt.Errorf("%w: Q-Block2 representation high-water does not match authenticated fragments",
			ErrInvalidOSCOREMessage)
	}
	if _, exists := state.seenResponsePartialIVs[highestPartialIV]; !exists {
		return fmt.Errorf("%w: Q-Block2 representation high-water is not tracked", ErrInvalidOSCOREMessage)
	}
	if state.hasCompletedQBlock2 && highestPartialIV <= state.completedQBlock2HighWater {
		return fmt.Errorf("%w: Q-Block2 representation is already retired", ErrReplay)
	}
	state.completedQBlock2HighWater = highestPartialIV
	state.hasCompletedQBlock2 = true
	for sequence := range state.seenResponsePartialIVs {
		if sequence <= highestPartialIV {
			delete(state.seenResponsePartialIVs, sequence)
		}
	}
	return nil
}

// HasResponse reports whether a response has been successfully authenticated
// for this exchange.
func (e Exchange) HasResponse() bool {
	if e.responseState == nil {
		return false
	}
	state := e.responseState
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.started
}

func (e Exchange) requestBinding() ([]byte, []byte, *Context) {
	if e.responseState == nil {
		return nil, nil, nil
	}
	state := e.responseState
	return state.requestID, state.requestPartialIV, state.context
}

func (e Exchange) matchesContext(context *Context) bool {
	return e.responseState != nil && context != nil && e.responseState.context == context
}

// CloneOptions returns a deep copy of options.
func CloneOptions(options message.Options) message.Options {
	cloned := make(message.Options, len(options))
	for i, option := range options {
		cloned[i] = message.Option{
			ID:    option.ID,
			Value: append([]byte(nil), option.Value...),
		}
	}
	return cloned
}

func isRequestCode(code codes.Code) bool {
	return code >= codes.GET && code <= codes.IPATCH
}

// IsResponseCode reports whether code is a CoAP response code.
func IsResponseCode(code codes.Code) bool {
	return code >= 64 && code < 192
}

func observeRegistrationFromOptions(options message.Options) (bool, error) {
	option, found, err := findObserveOption(options)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	value, err := observeOptionValue(option)
	if err != nil || value > 1 {
		return false, fmt.Errorf("%w: Observe request value must be 0 or 1", ErrInvalidOSCOREMessage)
	}
	return value == 0, nil
}

func mergeOptions(first, second message.Options) message.Options {
	options := append(CloneOptions(first), CloneOptions(second)...)
	sort.SliceStable(options, func(i, j int) bool {
		return options[i].ID < options[j].ID
	})
	return options
}

func mergeClassUOptions(outer, inner message.Options) message.Options {
	classUOptions := make(message.Options, 0, len(outer))
	for _, option := range outer {
		if ClassifyOption(option.ID) == ClassU {
			classUOptions = append(classUOptions, option)
		}
	}
	return mergeOptions(classUOptions, inner)
}

func appendOSCOREOptions(outer message.Options, encodedOption []byte) message.Options {
	outer = CloneOptions(outer)
	outer = outer.Add(message.Option{
		ID:    message.OSCORE,
		Value: append([]byte(nil), encodedOption...),
	})
	return outer
}

func parseOSCOREOuter(options message.Options) (OptionValue, message.Options, error) {
	var (
		optionValue     OptionValue
		optionFound     bool
		outerUserFields = make(message.Options, 0, len(options))
	)
	for _, option := range options {
		switch option.ID {
		case message.OSCORE:
			if optionFound {
				return OptionValue{}, nil, fmt.Errorf("%w: %w: duplicate OSCORE option", ErrInvalidOSCOREMessage, ErrInvalidOSCOREOption)
			}
			optionFound = true
			var err error
			optionValue, err = UnmarshalOption(option.Value)
			if err != nil {
				return OptionValue{}, nil, fmt.Errorf("%w: %w: decode OSCORE option: %w", ErrInvalidOSCOREMessage, ErrInvalidOSCOREOption, err)
			}
		default:
			outerUserFields = append(outerUserFields, option)
		}
	}
	if !optionFound {
		return OptionValue{}, nil, fmt.Errorf("%w: missing OSCORE option", ErrInvalidOSCOREMessage)
	}
	outerUserFields = discardSurplusHopLimitOptions(outerUserFields)
	_, err := SplitOptions(outerUserFields)
	if err != nil {
		return OptionValue{}, nil, err
	}
	filteredOuterFields := make(message.Options, 0, len(outerUserFields))
	for _, option := range outerUserFields {
		if option.ID == message.Observe || ClassifyOption(option.ID) != ClassE {
			filteredOuterFields = append(filteredOuterFields, option)
		}
	}
	return optionValue, CloneOptions(filteredOuterFields), nil
}

func discardSurplusHopLimitOptions(options message.Options) message.Options {
	result := make(message.Options, 0, len(options))
	hopLimitSeen := false
	for _, option := range options {
		if option.ID == message.HopLimit {
			if hopLimitSeen {
				continue
			}
			hopLimitSeen = true
		}
		result = append(result, option)
	}
	return result
}

func (c *Context) requestOption(partialIV []byte) ([]byte, error) {
	idContext, idContextSet := c.IDContext()
	return (OptionValue{
		PartialIV:    partialIV,
		IDContext:    idContext,
		SenderID:     c.SenderID(),
		IDContextSet: idContextSet,
		SenderIDSet:  true,
	}).MarshalBinary()
}

func (c *Context) responseOption(partialIV []byte) ([]byte, error) {
	return (OptionValue{PartialIV: partialIV}).MarshalBinary()
}

func (c *Context) validateRequestOption(option OptionValue) error {
	if len(option.PartialIV) == 0 || !option.SenderIDSet {
		return fmt.Errorf("%w: %w: request OSCORE option must contain Partial IV and KID", ErrInvalidOSCOREMessage, ErrInvalidOSCOREOption)
	}
	if !bytes.Equal(option.SenderID, c.recipientID) {
		return fmt.Errorf("%w: request KID does not match recipient context", ErrInvalidContext)
	}
	if option.IDContextSet && (!c.idContextSet || !bytes.Equal(option.IDContext, c.idContext)) {
		return fmt.Errorf("%w: request ID Context does not match recipient context", ErrInvalidContext)
	}
	return nil
}

func (c *Context) validateResponseOption(option OptionValue) error {
	if option.SenderIDSet || option.IDContextSet {
		return fmt.Errorf("%w: response OSCORE option must not contain KID or ID Context", ErrInvalidOSCOREMessage)
	}
	return nil
}

// ProtectRequest protects an outgoing CoAP request and returns the Exchange
// that must be retained for response processing. Keep the Exchange with the
// request; retransmissions should reuse the protected message rather than
// protecting a new request with the same application exchange.
func (c *Context) ProtectRequest(request message.Message) (message.Message, Exchange, error) {
	if !isRequestCode(request.Code) {
		return message.Message{}, Exchange{}, fmt.Errorf("%w: code %d is not a request", ErrInvalidOSCOREMessage, request.Code)
	}
	observeOption, observes, observeRegistration, split, plaintext, err := prepareInnerRequest(request)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}

	var exchange Exchange
	partialIV, ciphertext, err := c.Seal(plaintext, func(partialIV []byte) ([]byte, error) {
		exchange = newRequestExchange(c, request, partialIV, observeRegistration)
		return BuildAADWithAlgorithm(c.algorithm, exchange.responseState.requestID, exchange.responseState.requestPartialIV, nil)
	})
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	encodedOption, err := c.requestOption(partialIV)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	protected := request
	protected.Token = append(message.Token(nil), request.Token...)
	protected.Code = codes.POST
	outerOptions := split.Outer
	if observes {
		protected.Code = codes.FETCH
		outerOptions = mergeOptions(outerOptions, message.Options{observeOption})
	}
	protected.Options = appendOSCOREOptions(outerOptions, encodedOption)
	protected.Payload = ciphertext
	return protected, exchange, nil
}

func prepareInnerRequest(
	request message.Message,
) (message.Option, bool, bool, OptionSplit, []byte, error) {
	observeOption, observes, err := findObserveOption(request.Options)
	if err != nil {
		return message.Option{}, false, false, OptionSplit{}, nil, err
	}
	if observes && request.Code != codes.GET && request.Code != codes.FETCH {
		return message.Option{}, false, false, OptionSplit{}, nil, ErrUnsupportedOption
	}
	observeRegistration, err := observeRegistrationFromOptions(request.Options)
	if err != nil {
		return message.Option{}, false, false, OptionSplit{}, nil, err
	}
	options, err := normalizeProxyURI(request.Options)
	if err != nil {
		return message.Option{}, false, false, OptionSplit{}, nil, err
	}
	split, err := SplitOptions(options)
	if err != nil {
		return message.Option{}, false, false, OptionSplit{}, nil, err
	}
	plaintext, err := EncodeInner(request.Code, split.Inner, request.Payload)
	if err != nil {
		return message.Option{}, false, false, OptionSplit{}, nil, err
	}
	return observeOption, observes, observeRegistration, split, plaintext, nil
}

func newRequestExchange(c *Context, request message.Message, partialIV []byte, observeRegistration bool) Exchange {
	return Exchange{
		responseState: &exchangeResponseState{
			requestToken:     append(message.Token(nil), request.Token...),
			hasRequestToken:  true,
			requestID:        c.SenderID(),
			requestPartialIV: append([]byte(nil), partialIV...),
			observe:          observeRegistration,
			qBlock2Observe:   observeRegistration && request.Options.HasOption(message.QBlock2),
			context:          c,
			multipleResponses: observeRegistration ||
				request.Options.HasOption(message.QBlock1) ||
				request.Options.HasOption(message.QBlock2),
		},
	}
}

// UnprotectRequest authenticates and decrypts an incoming OSCORE request and
// returns the Exchange required to protect its response.
func (c *Context) UnprotectRequest(request message.Message) (message.Message, Exchange, error) {
	option, outerOptions, err := parseOSCOREOuter(request.Options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	outerObserveOption, hasOuterObserve, err := findObserveOption(outerOptions)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	if optionErr := c.validateRequestOption(option); optionErr != nil {
		return message.Message{}, Exchange{}, optionErr
	}
	outerOptions = removeObserveOption(outerOptions)
	exchange := Exchange{
		responseState: &exchangeResponseState{
			requestToken:     append(message.Token(nil), request.Token...),
			hasRequestToken:  true,
			requestID:        append([]byte(nil), option.SenderID...),
			requestPartialIV: append([]byte(nil), option.PartialIV...),
			context:          c,
		},
	}
	inner, hasInnerObserve, err := c.unprotectRequestInner(
		request, option, outerObserveOption, hasOuterObserve, exchange,
	)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	if hasInnerObserve {
		observeValue, _ := inner.Options.Observe()
		exchange.responseState.observe = observeValue == 0
	}
	exchange.responseState.multipleResponses = exchange.IsObserve() ||
		inner.Options.HasOption(message.QBlock1) ||
		inner.Options.HasOption(message.QBlock2)
	exchange.responseState.qBlock2Observe = exchange.IsObserve() && inner.Options.HasOption(message.QBlock2)
	inner.Token = append(message.Token(nil), request.Token...)
	inner.Type = request.Type
	inner.MessageID = request.MessageID
	inner.Options = mergeClassUOptions(outerOptions, inner.Options)
	return inner, exchange, nil
}

func (c *Context) unprotectRequestInner(
	request message.Message,
	option OptionValue,
	outerObserveOption message.Option,
	hasOuterObserve bool,
	exchange Exchange,
) (message.Message, bool, error) {
	sequence, err := decodePartialIV(option.PartialIV)
	if err != nil {
		return message.Message{}, false, err
	}
	nonce, err := c.nonce(option.PartialIV, c.recipientID)
	if err != nil {
		return message.Message{}, false, err
	}
	aad, err := BuildAADWithAlgorithm(c.algorithm, option.SenderID, option.PartialIV, nil)
	if err != nil {
		return message.Message{}, false, err
	}
	var (
		inner           message.Message
		hasInnerObserve bool
	)
	verify := func() error {
		plaintext, openErr := c.recipientAEAD.Open(nil, nonce, request.Payload, aad)
		if openErr != nil {
			return openErr
		}
		inner, openErr = DecodeInner(plaintext)
		if openErr != nil {
			return openErr
		}
		hasInnerObserve, openErr = validateInnerRequest(inner, outerObserveOption, hasOuterObserve)
		return openErr
	}
	err = c.acceptRequestSequence(sequence, verify, func() error {
		return c.validateReplayRecoveryEcho(inner.Options, exchange)
	})
	if err != nil {
		return message.Message{}, false, err
	}
	return inner, hasInnerObserve, nil
}

func (c *Context) acceptRequestSequence(sequence uint64, verify, checkEcho func() error) error {
	c.replayRecoveryMu.Lock()
	if !c.replayRecoveryPending {
		c.replayRecoveryMu.Unlock()
		return c.replay.Accept(sequence, func() error {
			if err := verify(); err != nil {
				return err
			}
			return checkEcho()
		})
	}
	err := c.replay.acceptRecovery(sequence, func() error {
		if err := verify(); err != nil {
			return err
		}
		return checkEcho()
	})
	if err == nil {
		c.replayRecoveryPending = false
	}
	c.replayRecoveryMu.Unlock()
	return err
}

func (c *Context) validateReplayRecoveryEcho(options message.Options, exchange Exchange) error {
	echo, found, err := findEchoOption(options)
	if err != nil {
		return err
	}
	if c.replayRecoveryPending {
		if !found || subtle.ConstantTimeCompare(echo.Value, c.replayRecoveryEcho) != 1 {
			return &replayRecoveryRequiredError{
				echo:     append([]byte(nil), c.replayRecoveryEcho...),
				exchange: exchange,
			}
		}
		return nil
	}
	return nil
}

func findEchoOption(options message.Options) (message.Option, bool, error) {
	start, end, err := options.Find(message.Echo)
	if err != nil {
		return message.Option{}, false, nil //nolint:nilerr
	}
	if end-start != 1 {
		return message.Option{}, false, fmt.Errorf("%w: duplicate Echo option", ErrInvalidOSCOREMessage)
	}
	option := options[start]
	return message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)}, true, nil
}

func validateInnerRequest(inner message.Message, outerObserveOption message.Option, hasOuterObserve bool) (bool, error) {
	if !isRequestCode(inner.Code) {
		return false, fmt.Errorf("%w: decrypted code %d is not a request", ErrInvalidOSCOREMessage, inner.Code)
	}
	_, hasInnerObserve, err := findObserveOption(inner.Options)
	if err != nil {
		return false, err
	}
	if hasInnerObserve != hasOuterObserve {
		return false, fmt.Errorf("%w: inner and outer Observe options must match", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve {
		innerObserve, _, err := findObserveOption(inner.Options)
		if err != nil {
			return false, err
		}
		outerValue, outerErr := observeOptionValue(outerObserveOption)
		innerValue, innerErr := observeOptionValue(innerObserve)
		if outerErr != nil || innerErr != nil || outerValue > 1 || innerValue > 1 || outerValue != innerValue {
			return false, fmt.Errorf("%w: inner and outer Observe options must match", ErrInvalidOSCOREMessage)
		}
		if inner.Code != codes.GET && inner.Code != codes.FETCH {
			return false, ErrUnsupportedOption
		}
	}
	return hasInnerObserve, nil
}

func findObserveOption(options message.Options) (message.Option, bool, error) {
	start, end, err := options.Find(message.Observe)
	if err != nil {
		// ErrOptionNotFound means the request is not an Observe exchange.
		return message.Option{}, false, nil //nolint:nilerr
	}
	if end-start != 1 {
		return message.Option{}, false, fmt.Errorf("%w: duplicate Observe option", ErrInvalidOSCOREMessage)
	}
	option := options[start]
	return message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)}, true, nil
}

func observeOptionValue(option message.Option) (uint32, error) {
	if len(option.Value) > 3 {
		return 0, fmt.Errorf("%w: invalid Observe option encoding", ErrInvalidOSCOREMessage)
	}
	return message.Options{option}.Observe()
}

func removeObserveOption(options message.Options) message.Options {
	return removeOption(options, message.Observe)
}

func removeOption(options message.Options, id message.OptionID) message.Options {
	result := make(message.Options, 0, len(options))
	for _, option := range options {
		if option.ID != id {
			result = append(result, option)
		}
	}
	return result
}

func setObserve(options message.Options, value uint32) (message.Options, error) {
	result, _, err := (message.Options{}).SetObserve(make([]byte, 4), value)
	if err != nil {
		return nil, err
	}
	return mergeOptions(removeObserveOption(options), result), nil
}

func containsProxyOption(options message.Options) bool {
	for _, option := range options {
		if option.ID == message.ProxyURI || option.ID == message.ProxyScheme {
			return true
		}
	}
	return false
}

func validateExchange(exchange Exchange) error {
	requestID, requestPartialIV, context := exchange.requestBinding()
	if exchange.responseState == nil || context == nil {
		return fmt.Errorf("%w: exchange has no authenticated request binding", ErrInvalidContext)
	}
	if len(requestID) > maxIdentifierSize {
		return fmt.Errorf("%w: request ID exceeds %d bytes", ErrInvalidOSCOREMessage, maxIdentifierSize)
	}
	if _, err := decodePartialIV(requestPartialIV); err != nil {
		return fmt.Errorf("%w: invalid exchange Partial IV: %w", ErrInvalidOSCOREMessage, err)
	}
	return nil
}

// ProtectResponse protects the ordinary response bound to exchange. It uses
// the request Partial IV as its nonce and therefore succeeds at most once per
// Exchange, including across copies and concurrent calls. Resend the returned
// protected message for retransmission.
func (c *Context) ProtectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	return c.protectResponse(response, exchange, false)
}

// ProtectResponseWithPartialIV protects a response with a fresh sender Partial
// IV. Unlike ProtectResponse, it consumes a sequence number and includes that
// Partial IV in the OSCORE option. The response AAD remains bound to the
// original request exchange. Repeated use is supported for Observe and
// Q-Block Exchanges; an ordinary Exchange remains one-shot.
func (c *Context) ProtectResponseWithPartialIV(response message.Message, exchange Exchange) (message.Message, error) {
	return c.protectResponse(response, exchange, true)
}

func (c *Context) protectResponse(response message.Message, exchange Exchange, usePartialIV bool) (message.Message, error) {
	if !IsResponseCode(response.Code) {
		return message.Message{}, fmt.Errorf("%w: code %d is not a response", ErrInvalidOSCOREMessage, response.Code)
	}
	if validationErr := validateExchange(exchange); validationErr != nil {
		return message.Message{}, validationErr
	}
	if !exchange.matchesContext(c) {
		return message.Message{}, fmt.Errorf("%w: exchange belongs to another security context", ErrInvalidContext)
	}
	observeOption, observes, split, plaintext, err := prepareResponsePlaintext(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	needsPartialIV := usePartialIV || exchange.IsObserve() || observes
	state := exchange.responseState
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.multipleResponses || !needsPartialIV {
		if state.responseProtected {
			return message.Message{}, ErrReplay
		}
	}
	partialIV, ciphertext, err := c.sealResponse(plaintext, exchange, needsPartialIV)
	if err != nil {
		return message.Message{}, err
	}
	if !state.multipleResponses || !needsPartialIV {
		state.responseProtected = true
	}
	encodedOption, err := c.responseOption(partialIV)
	if err != nil {
		return message.Message{}, err
	}
	protected := response
	protected.Token = append(message.Token(nil), response.Token...)
	protected.Code = outerResponseCode
	outerOptions := split.Outer
	if observes {
		protected.Code = codes.Content
		outerOptions = mergeOptions(outerOptions, message.Options{observeOption})
	}
	protected.Options = appendOSCOREOptions(outerOptions, encodedOption)
	protected.Payload = ciphertext
	return protected, nil
}

func prepareResponsePlaintext(response message.Message, exchange Exchange) (message.Option, bool, OptionSplit, []byte, error) {
	observeOption, observes, err := findObserveOption(response.Options)
	if err != nil {
		return message.Option{}, false, OptionSplit{}, nil, err
	}
	if observes {
		if _, observeErr := observeOptionValue(observeOption); observeErr != nil {
			return message.Option{}, false, OptionSplit{}, nil, fmt.Errorf("%w: invalid Observe response option: %w", ErrInvalidOSCOREMessage, observeErr)
		}
		if !exchange.IsObserve() {
			return message.Option{}, false, OptionSplit{}, nil, fmt.Errorf("%w: Observe response for a non-Observe request", ErrInvalidOSCOREMessage)
		}
	}
	err = validateExchange(exchange)
	if err != nil {
		return message.Option{}, false, OptionSplit{}, nil, err
	}
	split, err := SplitOptions(response.Options)
	if err != nil {
		return message.Option{}, false, OptionSplit{}, nil, err
	}
	if containsProxyOption(split.Outer) {
		return message.Option{}, false, OptionSplit{}, nil, ErrUnsupportedOption
	}
	innerOptions := split.Inner
	if observes {
		innerOptions, err = setObserve(innerOptions, 0)
		if err != nil {
			return message.Option{}, false, OptionSplit{}, nil, err
		}
	}
	plaintext, err := EncodeInner(response.Code, innerOptions, response.Payload)
	if err != nil {
		return message.Option{}, false, OptionSplit{}, nil, err
	}
	return observeOption, observes, split, plaintext, nil
}

func (c *Context) sealResponse(plaintext []byte, exchange Exchange, usePartialIV bool) ([]byte, []byte, error) {
	requestID, requestPartialIV, _ := exchange.requestBinding()
	buildAAD := func([]byte) ([]byte, error) {
		return BuildAADWithAlgorithm(c.algorithm, requestID, requestPartialIV, nil)
	}
	if usePartialIV {
		return c.SealResponse(plaintext, buildAAD)
	}
	ciphertext, err := c.SealResponseWithRequestNonce(plaintext, requestPartialIV, buildAAD)
	return nil, ciphertext, err
}

// UnprotectResponse authenticates and decrypts a response bound to exchange.
// The Exchange must have been created by this Context; it remains valid after
// the Context is removed from a Processor because the Exchange retains that
// Context for in-flight response processing.
// Ordinary responses without a Partial IV and duplicate response Partial IVs
// are accepted at most once per Exchange. Failed authentication does not
// consume the Exchange.
func (c *Context) UnprotectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	option, _, err := parseOSCOREOuter(response.Options)
	if err != nil {
		return message.Message{}, err
	}
	if validationErr := validateExchange(exchange); validationErr != nil {
		return message.Message{}, validationErr
	}
	if !exchange.matchesContext(c) {
		return message.Message{}, fmt.Errorf("%w: exchange belongs to another security context", ErrInvalidContext)
	}
	state := exchange.responseState
	if state.hasRequestToken && !bytes.Equal(response.Token, state.requestToken) {
		return message.Message{}, fmt.Errorf("%w: response Token does not match request", ErrInvalidOSCOREMessage)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	responseSequence, err := state.checkResponse(exchange, option)
	if err != nil {
		return message.Message{}, err
	}
	plaintext, err := c.unprotectResponse(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	state.recordResponse(exchange, option, responseSequence, plaintext)
	return plaintext, nil
}

func (state *exchangeResponseState) checkResponse(exchange Exchange, option OptionValue) (uint64, error) {
	if state.observeComplete || state.started && !state.multipleResponses {
		return 0, ErrReplay
	}
	if len(option.PartialIV) == 0 {
		if state.started {
			return 0, ErrReplay
		}
		return 0, nil
	}
	responseSequence, err := decodePartialIV(option.PartialIV)
	if err != nil {
		return 0, err
	}
	if exchange.IsObserve() {
		if err := state.checkObserveResponseSequence(responseSequence); err != nil {
			return 0, err
		}
	} else if state.multipleResponses && state.seenResponsePartialIVs != nil {
		if _, exists := state.seenResponsePartialIVs[responseSequence]; exists {
			return 0, ErrReplay
		}
	}
	return responseSequence, nil
}

func (state *exchangeResponseState) checkObserveResponseSequence(sequence uint64) error {
	if state.qBlock2Observe {
		return state.checkQBlock2ObserveSequence(sequence)
	}
	if state.hasResponseSequence && sequence <= state.lastResponseSequence {
		return ErrReplay
	}
	return nil
}

func (state *exchangeResponseState) checkQBlock2ObserveSequence(sequence uint64) error {
	if _, exists := state.seenResponsePartialIVs[sequence]; exists {
		return ErrReplay
	}
	if state.hasCompletedQBlock2 && sequence <= state.completedQBlock2HighWater {
		return ErrReplay
	}
	if uint64(len(state.seenResponsePartialIVs)) >= state.qBlock2ObserveWindowSize() {
		return ErrReplayTrackingLimit
	}
	return nil
}

func (state *exchangeResponseState) recordResponse(
	exchange Exchange,
	option OptionValue,
	responseSequence uint64,
	response message.Message,
) {
	state.started = true
	if exchange.IsObserve() {
		state.recordObserveResponse(option, responseSequence, response)
	} else if state.multipleResponses && len(option.PartialIV) > 0 {
		state.recordSeenResponseSequence(responseSequence)
	}
}

func (state *exchangeResponseState) recordObserveResponse(
	option OptionValue,
	responseSequence uint64,
	response message.Message,
) {
	hasObserve := response.Options.HasOption(message.Observe)
	if !hasObserve && (!state.qBlock2Observe || !response.Options.HasOption(message.QBlock2)) {
		state.observeComplete = true
	}
	if len(option.PartialIV) == 0 {
		return
	}
	if state.qBlock2Observe {
		state.recordSeenResponseSequence(responseSequence)
		if !state.hasResponseSequence || responseSequence > state.lastResponseSequence {
			state.lastResponseSequence = responseSequence
		}
	}
	state.hasResponseSequence = true
	if !state.qBlock2Observe {
		state.lastResponseSequence = responseSequence
	}
}

func (state *exchangeResponseState) recordSeenResponseSequence(responseSequence uint64) {
	if state.seenResponsePartialIVs == nil {
		state.seenResponsePartialIVs = make(map[uint64]struct{})
	}
	state.seenResponsePartialIVs[responseSequence] = struct{}{}
}

func (state *exchangeResponseState) qBlock2ObserveWindowSize() uint64 {
	if state.context != nil && state.context.qBlock2ObserveWindowSize > 0 {
		return state.context.qBlock2ObserveWindowSize
	}
	return 1024
}

func (c *Context) unprotectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	option, outerOptions, inner, err := c.openResponse(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	_, hasOuterObserve, err := findObserveOption(outerOptions)
	if err != nil {
		return message.Message{}, err
	}
	inner.Options, err = validateInnerResponse(inner, exchange, option, hasOuterObserve)
	if err != nil {
		return message.Message{}, err
	}
	inner.Token = append(message.Token(nil), response.Token...)
	inner.Type = response.Type
	inner.MessageID = response.MessageID
	outerOptions = removeObserveOption(outerOptions)
	outerOptions = removeOption(outerOptions, message.Echo)
	if inner.Options.HasOption(message.MaxAge) {
		outerOptions = removeOption(outerOptions, message.MaxAge)
	}
	inner.Options = mergeClassUOptions(outerOptions, inner.Options)
	return inner, nil
}

func (c *Context) openResponse(response message.Message, exchange Exchange) (OptionValue, message.Options, message.Message, error) {
	if err := validateExchange(exchange); err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	option, outerOptions, err := parseOSCOREOuter(response.Options)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	if optionErr := c.validateResponseOption(option); optionErr != nil {
		return OptionValue{}, nil, message.Message{}, optionErr
	}
	if containsProxyOption(outerOptions) {
		return OptionValue{}, nil, message.Message{}, ErrUnsupportedOption
	}
	outerObserveOption, hasOuterObserve, observeErr := findObserveOption(outerOptions)
	if observeErr != nil {
		return OptionValue{}, nil, message.Message{}, observeErr
	}
	if hasOuterObserve {
		if _, observeErr := observeOptionValue(outerObserveOption); observeErr != nil {
			return OptionValue{}, nil, message.Message{}, observeErr
		}
	}
	requestID, requestPartialIV, _ := exchange.requestBinding()
	aad, err := BuildAADWithAlgorithm(c.algorithm, requestID, requestPartialIV, nil)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	plaintext, err := c.OpenResponseForExchange(option.PartialIV, requestPartialIV, response.Payload, aad)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	inner, err := DecodeInner(plaintext)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	return option, outerOptions, inner, nil
}

func validateInnerResponse(
	inner message.Message,
	exchange Exchange,
	option OptionValue,
	hasOuterObserve bool,
) (message.Options, error) {
	if !IsResponseCode(inner.Code) {
		return nil, fmt.Errorf("%w: decrypted code %d is not a response", ErrInvalidOSCOREMessage, inner.Code)
	}
	innerObserve, hasInnerObserve, err := findObserveOption(inner.Options)
	if err != nil {
		return nil, err
	}
	if err := validateInnerObserveForExchange(innerObserve, hasInnerObserve, exchange, hasOuterObserve); err != nil {
		return nil, err
	}
	if !hasInnerObserve {
		return inner.Options, nil
	}
	if len(option.PartialIV) == 0 {
		return setInnerObserveWithoutPartialIV(inner)
	}
	return setInnerObserveFromPartialIV(inner, option.PartialIV)
}

func validateInnerObserveForExchange(
	innerObserve message.Option,
	hasInnerObserve bool,
	exchange Exchange,
	hasOuterObserve bool,
) error {
	if hasInnerObserve && !exchange.IsObserve() {
		return fmt.Errorf("%w: Observe response for a non-Observe request", ErrInvalidOSCOREMessage)
	}
	if hasOuterObserve && !hasInnerObserve {
		return fmt.Errorf("%w: Outer Observe response has no protected Inner Observe option", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve && len(innerObserve.Value) != 0 {
		return fmt.Errorf("%w: inner Observe notification value must be empty", ErrInvalidOSCOREMessage)
	}
	return nil
}

func setInnerObserveWithoutPartialIV(inner message.Message) (message.Options, error) {
	var err error
	inner.Options, err = setObserve(inner.Options, 0)
	if err != nil {
		return nil, err
	}
	return inner.Options, nil
}

func setInnerObserveFromPartialIV(inner message.Message, partialIV []byte) (message.Options, error) {
	if len(partialIV) > 3 {
		partialIV = partialIV[len(partialIV)-3:]
	}
	sequence := uint32(0)
	for i := range partialIV {
		sequence = sequence<<8 | uint32(partialIV[i])
	}
	var err error
	inner.Options, err = setObserve(inner.Options, sequence)
	if err != nil {
		return nil, err
	}
	return inner.Options, nil
}
