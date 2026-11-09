package oscore

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

var (
	ErrInvalidOSCOREMessage = errors.New("invalid OSCORE message")
	ErrUnsupportedOption    = errors.New("unsupported OSCORE option processing")
)

const outerResponseCode = codes.Changed

// Exchange binds a response to the request that established its OSCORE AAD
// and nonce context. Keep it with the CoAP token until the exchange completes.
type Exchange struct {
	RequestID        []byte
	RequestPartialIV []byte
	Observe          bool
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
	split, err := SplitOptions(outerUserFields)
	if err != nil {
		return OptionValue{}, nil, err
	}
	for _, option := range split.Inner {
		if option.ID != message.Observe {
			return OptionValue{}, nil, fmt.Errorf("%w: Class E option in outer message", ErrInvalidOSCOREMessage)
		}
	}
	return optionValue, cloneOptions(outerUserFields), nil
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
	if observes {
		value, observeErr := request.Options.Observe()
		if observeErr != nil || value > 1 {
			return message.Message{}, Exchange{}, fmt.Errorf("%w: Observe request value must be 0 or 1", ErrInvalidOSCOREMessage)
		}
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
			RequestID:        c.SenderID(),
			RequestPartialIV: append([]byte(nil), partialIV...),
			Observe:          observes,
		}
		return BuildAADWithAlgorithm(c.algorithm, exchange.RequestID, exchange.RequestPartialIV, nil)
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

func (c *Context) UnprotectRequest(request message.Message) (message.Message, Exchange, error) {
	option, outerOptions, err := parseOSCOREOuter(request.Options)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	outerObserveOption, hasOuterObserve, err := findObserveOption(outerOptions)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	err = validateOuterRequestCode(request.Code, hasOuterObserve)
	if err != nil {
		return message.Message{}, Exchange{}, err
	}
	if optionErr := c.validateRequestOption(option); optionErr != nil {
		return message.Message{}, Exchange{}, optionErr
	}
	outerOptions = removeOption(outerOptions, message.Observe)
	exchange := Exchange{
		RequestID:        append([]byte(nil), option.SenderID...),
		RequestPartialIV: append([]byte(nil), option.PartialIV...),
	}
	aad, err := BuildAADWithAlgorithm(c.algorithm, exchange.RequestID, exchange.RequestPartialIV, nil)
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
	exchange.Observe = hasInnerObserve
	inner.Token = append(message.Token(nil), request.Token...)
	inner.Type = request.Type
	inner.MessageID = request.MessageID
	inner.Options = mergeOptions(outerOptions, inner.Options)
	return inner, exchange, nil
}

func validateOuterRequestCode(code codes.Code, hasObserve bool) error {
	if hasObserve {
		if code != codes.FETCH {
			return fmt.Errorf("%w: outer request code with Observe must be FETCH", ErrInvalidOSCOREMessage)
		}
	} else if code != codes.POST {
		return fmt.Errorf("%w: outer request code must be POST", ErrInvalidOSCOREMessage)
	}
	return nil
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
		outerValue, outerErr := message.Options{outerObserveOption}.Observe()
		innerValue, innerErr := inner.Options.Observe()
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
	return mergeOptions(removeOption(options, message.Observe), result), nil
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
	if len(exchange.RequestID) > maxIdentifierSize {
		return fmt.Errorf("%w: request ID exceeds %d bytes", ErrInvalidOSCOREMessage, maxIdentifierSize)
	}
	if _, err := decodePartialIV(exchange.RequestPartialIV); err != nil {
		return fmt.Errorf("%w: invalid exchange Partial IV: %w", ErrInvalidOSCOREMessage, err)
	}
	return nil
}

func (c *Context) ProtectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	return c.protectResponse(response, exchange, false)
}

// ProtectResponseWithPartialIV protects a response with a fresh sender Partial
// IV. Unlike ProtectResponse, it consumes a sequence number and includes that
// Partial IV in the OSCORE option. The response AAD remains bound to the
// original request exchange.
func (c *Context) ProtectResponseWithPartialIV(response message.Message, exchange Exchange) (message.Message, error) {
	return c.protectResponse(response, exchange, true)
}

func (c *Context) protectResponse(response message.Message, exchange Exchange, usePartialIV bool) (message.Message, error) {
	if !isResponseCode(response.Code) {
		return message.Message{}, fmt.Errorf("%w: code %d is not a response", ErrInvalidOSCOREMessage, response.Code)
	}
	observeOption, observes, split, plaintext, err := prepareResponsePlaintext(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	partialIV, ciphertext, err := c.sealResponse(plaintext, exchange, usePartialIV || exchange.Observe || observes)
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
		if _, observeErr := response.Options.Observe(); observeErr != nil {
			return message.Option{}, false, OptionSplit{}, nil, fmt.Errorf("%w: invalid Observe response option: %w", ErrInvalidOSCOREMessage, observeErr)
		}
		if !exchange.Observe {
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
	buildAAD := func([]byte) ([]byte, error) {
		return BuildAADWithAlgorithm(c.algorithm, exchange.RequestID, exchange.RequestPartialIV, nil)
	}
	if usePartialIV {
		return c.SealResponse(plaintext, buildAAD)
	}
	ciphertext, err := c.SealResponseWithRequestNonce(plaintext, exchange.RequestPartialIV, buildAAD)
	return nil, ciphertext, err
}

func (c *Context) UnprotectResponse(response message.Message, exchange Exchange) (message.Message, error) {
	option, outerOptions, inner, err := c.openResponse(response, exchange)
	if err != nil {
		return message.Message{}, err
	}
	inner.Options, err = validateInnerResponse(inner, response, exchange, option)
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
	if response.Code != outerResponseCode && response.Code != codes.Content {
		return OptionValue{}, nil, message.Message{}, fmt.Errorf("%w: unexpected outer response code %d", ErrInvalidOSCOREMessage, response.Code)
	}
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
	if _, _, observeErr := findObserveOption(outerOptions); observeErr != nil {
		return OptionValue{}, nil, message.Message{}, observeErr
	}
	outerOptions = removeOption(outerOptions, message.Observe)
	aad, err := BuildAADWithAlgorithm(c.algorithm, exchange.RequestID, exchange.RequestPartialIV, nil)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	plaintext, err := c.OpenResponseForExchange(option.PartialIV, exchange.RequestPartialIV, response.Payload, aad)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	inner, err := DecodeInner(plaintext)
	if err != nil {
		return OptionValue{}, nil, message.Message{}, err
	}
	return option, outerOptions, inner, nil
}

func validateInnerResponse(inner message.Message, response message.Message, exchange Exchange, option OptionValue) (message.Options, error) {
	if !isResponseCode(inner.Code) {
		return nil, fmt.Errorf("%w: decrypted code %d is not a response", ErrInvalidOSCOREMessage, inner.Code)
	}
	innerObserve, hasInnerObserve, err := findObserveOption(inner.Options)
	if err != nil {
		return nil, err
	}
	if hasInnerObserve && !exchange.Observe {
		return nil, fmt.Errorf("%w: Observe response for a non-Observe request", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve && len(innerObserve.Value) != 0 {
		return nil, fmt.Errorf("%w: inner Observe notification value must be empty", ErrInvalidOSCOREMessage)
	}
	if hasInnerObserve && response.Code != codes.Content {
		return nil, fmt.Errorf("%w: Observe response outer code must be Content", ErrInvalidOSCOREMessage)
	}
	if !hasInnerObserve && response.Code != outerResponseCode {
		return nil, fmt.Errorf("%w: non-Observe response outer code must be Changed", ErrInvalidOSCOREMessage)
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
