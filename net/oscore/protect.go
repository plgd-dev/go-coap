package oscore

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

var (
	ErrInvalidOSCOREMessage = errors.New("invalid OSCORE message")
	ErrUnsupportedOption    = errors.New("unsupported OSCORE option processing")
)

const outerResponseCode = codes.Changed

// Exchange binds a response to the request that established its OSCORE AAD
// and nonce context. Keep the exact value returned by ProtectRequest or
// UnprotectRequest with the CoAP token until the exchange completes; response
// replay state is shared by copies of this value.
type Exchange struct {
	responseState *exchangeResponseState
}

type exchangeResponseState struct {
	mu                     sync.Mutex
	requestID              []byte
	requestPartialIV       []byte
	observe                bool
	context                *Context
	started                bool
	observeComplete        bool
	multipleResponses      bool
	responseProtected      bool
	hasNotificationNumber  bool
	notificationNumber     uint64
	seenResponsePartialIVs map[uint64]struct{}
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

func cloneOptions(options message.Options) message.Options {
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

func isResponseCode(code codes.Code) bool {
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
	options := append(cloneOptions(first), cloneOptions(second)...)
	sort.SliceStable(options, func(i, j int) bool {
		return options[i].ID < options[j].ID
	})
	return options
}

func appendOSCOREOptions(outer message.Options, encodedOption []byte) message.Options {
	outer = cloneOptions(outer)
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
				return OptionValue{}, nil, fmt.Errorf("%w: duplicate OSCORE option", ErrInvalidOSCOREMessage)
			}
			optionFound = true
			var err error
			optionValue, err = UnmarshalOption(option.Value)
			if err != nil {
				return OptionValue{}, nil, fmt.Errorf("%w: decode OSCORE option: %w", ErrInvalidOSCOREMessage, err)
			}
		default:
			outerUserFields = append(outerUserFields, option)
		}
	}
	if !optionFound {
		return OptionValue{}, nil, fmt.Errorf("%w: missing OSCORE option", ErrInvalidOSCOREMessage)
	}
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
	return optionValue, cloneOptions(filteredOuterFields), nil
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
		return fmt.Errorf("%w: request OSCORE option must contain Partial IV and KID", ErrInvalidOSCOREMessage)
	}
	if !bytes.Equal(option.SenderID, c.recipientID) {
		return fmt.Errorf("%w: request KID does not match recipient context", ErrInvalidContext)
	}
	if option.IDContextSet != c.idContextSet || (option.IDContextSet && !bytes.Equal(option.IDContext, c.idContext)) {
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
	observeOption, observes, err := findObserveOption(request.Options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	if observes && request.Code != codes.GET && request.Code != codes.FETCH {
		return message.Message{}, Exchange{}, ErrUnsupportedOption
	}
	observeRegistration, err := observeRegistrationFromOptions(request.Options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	options, err := normalizeProxyURI(request.Options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	split, err := SplitOptions(options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	plaintext, err := EncodeInner(request.Code, split.Inner, request.Payload)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}

	var exchange Exchange
	partialIV, ciphertext, err := c.Seal(plaintext, func(partialIV []byte) ([]byte, error) {
		exchange = Exchange{
			responseState: &exchangeResponseState{
				requestID:        c.SenderID(),
				requestPartialIV: append([]byte(nil), partialIV...),
				observe:          observeRegistration,
				context:          c,
				multipleResponses: observeRegistration ||
					request.Options.HasOption(message.QBlock1) ||
					request.Options.HasOption(message.QBlock2),
			},
		}
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
			requestID:        append([]byte(nil), option.SenderID...),
			requestPartialIV: append([]byte(nil), option.PartialIV...),
			context:          c,
		},
	}
	aad, err := BuildAADWithAlgorithm(c.algorithm, exchange.responseState.requestID, exchange.responseState.requestPartialIV, nil)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	plaintext, err := c.Open(option.PartialIV, request.Payload, aad)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	inner, err := DecodeInner(plaintext)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	hasInnerObserve, err := validateInnerRequest(inner, outerObserveOption, hasOuterObserve)
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
	inner.Token = append(message.Token(nil), request.Token...)
	inner.Type = request.Type
	inner.MessageID = request.MessageID
	inner.Options = mergeOptions(outerOptions, inner.Options)
	return inner, exchange, nil
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
	if len(option.Value) > 3 || len(option.Value) > 0 && option.Value[0] == 0 {
		return 0, fmt.Errorf("%w: invalid Observe option encoding", ErrInvalidOSCOREMessage)
	}
	return message.Options{option}.Observe()
}

func removeObserveOption(options message.Options) message.Options {
	result := make(message.Options, 0, len(options))
	for _, option := range options {
		if option.ID != message.Observe {
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
	if !isResponseCode(response.Code) {
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
		state.responseProtected = true
	}
	partialIV, ciphertext, err := c.sealResponse(plaintext, exchange, needsPartialIV)
	if err != nil {
		return message.Message{}, err
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
	state.recordResponse(exchange, option, responseSequence, plaintext.Options.HasOption(message.Observe))
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
		if state.hasNotificationNumber && responseSequence <= state.notificationNumber {
			return 0, ErrReplay
		}
	} else if state.multipleResponses && state.seenResponsePartialIVs != nil {
		if _, exists := state.seenResponsePartialIVs[responseSequence]; exists {
			return 0, ErrReplay
		}
	}
	return responseSequence, nil
}

func (state *exchangeResponseState) recordResponse(
	exchange Exchange,
	option OptionValue,
	responseSequence uint64,
	hasObserve bool,
) {
	state.started = true
	if exchange.IsObserve() {
		if !hasObserve {
			state.observeComplete = true
		}
		if len(option.PartialIV) > 0 {
			state.hasNotificationNumber = true
			state.notificationNumber = responseSequence
		}
	} else if state.multipleResponses && len(option.PartialIV) > 0 {
		if state.seenResponsePartialIVs == nil {
			state.seenResponsePartialIVs = make(map[uint64]struct{})
		}
		state.seenResponsePartialIVs[responseSequence] = struct{}{}
	}
}

func (c *Context) unprotectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	option, outerOptions, inner, err := c.openResponse(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	inner.Options, err = validateInnerResponse(inner, exchange, option)
	if err != nil {
		return message.Message{}, err
	}
	inner.Token = append(message.Token(nil), response.Token...)
	inner.Type = response.Type
	inner.MessageID = response.MessageID
	inner.Options = mergeOptions(outerOptions, inner.Options)
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
	outerOptions = removeObserveOption(outerOptions)
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

func validateInnerResponse(inner message.Message, exchange Exchange, option OptionValue) (message.Options, error) {
	if !isResponseCode(inner.Code) {
		return nil, fmt.Errorf("%w: decrypted code %d is not a response", ErrInvalidOSCOREMessage, inner.Code)
	}
	innerObserve, hasInnerObserve, err := findObserveOption(inner.Options)
	if err != nil {
		return nil, err
	}
	if hasInnerObserve && !exchange.IsObserve() {
		return nil, fmt.Errorf("%w: Observe response for a non-Observe request", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve && len(innerObserve.Value) != 0 {
		return nil, fmt.Errorf("%w: inner Observe notification value must be empty", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve {
		sequence := uint32(0x00ffffff)
		if len(option.PartialIV) > 0 {
			sequence = 0
			for _, b := range option.PartialIV {
				sequence = (sequence << 8) | uint32(b)
			}
			sequence &= 0x00ffffff
		}
		inner.Options, err = setObserve(inner.Options, sequence)
		if err != nil {
			return nil, err
		}
	}
	return inner.Options, nil
}
