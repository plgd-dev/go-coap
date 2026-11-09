package oscore

import (
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
)

// OptionClass identifies the protection applied to a CoAP option field.
type OptionClass uint8

const (
	// ClassE options are encrypted and integrity protected.
	ClassE OptionClass = iota
	// ClassI options are carried outside the ciphertext and integrity protected
	// through the external AAD. RFC 8613 currently defines no Class I options.
	ClassI
	// ClassU options are carried outside the ciphertext without OSCORE protection.
	ClassU
)

var (
	ErrNestedOSCOREOption  = errors.New("nested OSCORE option is not supported")
	ErrInvalidOptionClass  = errors.New("invalid OSCORE option class")
	ErrInvalidInnerMessage = errors.New("invalid OSCORE inner message")
)

// ClassifyOption returns the RFC 8613 default class for an option. Options with
// only Class U processing in RFC 8613 remain outer. Options that support both
// inner and outer use are classified as Class E here; specialized proxy or
// block-wise processing may explicitly choose the outer form.
//
// Unknown options default to Class E, as required by RFC 8613.
func ClassifyOption(id message.OptionID) OptionClass {
	switch id {
	case message.URIHost, message.URIPort, message.OSCORE, message.ProxyURI, message.ProxyScheme:
		return ClassU
	default:
		return ClassE
	}
}

// OptionSplit separates inner Class E and outer Class U options. No Class I
// options are currently defined by RFC 8613. The returned option values alias
// the input; callers must preserve their lifetime.
type OptionSplit struct {
	Inner message.Options
	Outer message.Options
}

func marshalOptions(options message.Options) ([]byte, error) {
	size, err := options.Marshal(nil)
	if err != nil && !errors.Is(err, message.ErrTooSmall) {
		return nil, err
	}
	encoded := make([]byte, size)
	if _, err := options.Marshal(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

// SplitOptions separates options according to their RFC 8613 default class.
func SplitOptions(options message.Options) (OptionSplit, error) {
	split := OptionSplit{
		Inner: make(message.Options, 0, len(options)),
		Outer: make(message.Options, 0, len(options)),
	}
	var previous message.OptionID
	for i, option := range options {
		if i > 0 && option.ID < previous {
			return OptionSplit{}, fmt.Errorf("%w: options are not sorted", ErrInvalidInnerMessage)
		}
		previous = option.ID
		if option.ID == message.OSCORE {
			return OptionSplit{}, ErrNestedOSCOREOption
		}
		switch ClassifyOption(option.ID) {
		case ClassE:
			split.Inner = append(split.Inner, option)
		case ClassU:
			split.Outer = append(split.Outer, option)
		case ClassI:
			return OptionSplit{}, fmt.Errorf("%w: Class I option %d has no AAD encoder", ErrInvalidOptionClass, option.ID)
		default:
			return OptionSplit{}, fmt.Errorf("%w: option %d", ErrInvalidOptionClass, option.ID)
		}
	}
	return split, nil
}

// EncodeInner serializes the CoAP request/response fields that RFC 8613 puts in
// the COSE plaintext: Code, Class E options, and optionally payload.
func EncodeInner(code codes.Code, options message.Options, payload []byte) ([]byte, error) {
	if code > 255 {
		return nil, fmt.Errorf("%w: code %d exceeds one byte", ErrInvalidInnerMessage, code)
	}
	split, err := SplitOptions(options)
	if err != nil {
		return nil, err
	}
	if len(split.Outer) != 0 {
		return nil, fmt.Errorf("%w: Class U options cannot be encoded as inner fields", ErrInvalidInnerMessage)
	}

	encodedOptions, err := marshalOptions(split.Inner)
	if err != nil {
		return nil, fmt.Errorf("marshal OSCORE inner options: %w", err)
	}
	result := make([]byte, 1, 1+len(encodedOptions)+len(payload)+1)
	result[0] = byte(code)
	result = append(result, encodedOptions...)
	if len(payload) > 0 {
		result = append(result, 0xff)
		result = append(result, payload...)
	}
	return result, nil
}

// DecodeInner parses a CoAP request/response plaintext produced by EncodeInner.
// CoAP messaging-layer fields (type, token, and message ID) are not present in
// the OSCORE plaintext and are left unset.
func DecodeInner(plaintext []byte) (message.Message, error) {
	if len(plaintext) == 0 {
		return message.Message{}, fmt.Errorf("%w: missing code", ErrInvalidInnerMessage)
	}

	decoded := message.Message{
		Code:      codes.Code(plaintext[0]),
		MessageID: -1,
		Type:      message.Unset,
	}
	optionsData := plaintext[1:]
	decoded.Options = make(message.Options, 0, len(optionsData))
	processed, err := decoded.Options.Unmarshal(optionsData, message.CoapOptionDefs)
	if err != nil {
		return message.Message{}, fmt.Errorf("%w: decode options: %w", ErrInvalidInnerMessage, err)
	}
	if processed > len(optionsData) {
		return message.Message{}, fmt.Errorf("%w: decoded option length exceeds plaintext", ErrInvalidInnerMessage)
	}
	if processed > 0 && optionsData[processed-1] == 0xff {
		decoded.Payload = append([]byte(nil), optionsData[processed:]...)
	}

	split, err := SplitOptions(decoded.Options)
	if err != nil {
		return message.Message{}, err
	}
	if len(split.Outer) != 0 {
		return message.Message{}, fmt.Errorf("%w: plaintext contains Class U option", ErrInvalidInnerMessage)
	}
	return decoded, nil
}

// BuildAAD creates the RFC 8613 COSE_Encrypt0 Enc_structure. requestKid and
// requestPartialIV identify the original request for response binding. options
// must contain Class I options in RFC 7252 option encoding order; the current
// RFC 8613 option set defines none, so this is normally empty.
func BuildAAD(requestKid, requestPartialIV []byte, options message.Options) ([]byte, error) {
	return BuildAADWithAlgorithm(AEADAlgorithmAESCCM16_64_128, requestKid, requestPartialIV, options)
}

// BuildAADWithAlgorithm creates the RFC 8613 COSE_Encrypt0 Enc_structure
// using algorithm. requestKid and requestPartialIV identify the original
// request for response binding.
func BuildAADWithAlgorithm(algorithm AEADAlgorithm, requestKid, requestPartialIV []byte, options message.Options) ([]byte, error) {
	if algorithmParameters(algorithm).keySize == 0 {
		return nil, fmt.Errorf("%w: COSE algorithm %d", ErrUnsupportedAlgorithm, algorithm)
	}
	if len(requestPartialIV) > 0 {
		if _, err := decodePartialIV(requestPartialIV); err != nil {
			return nil, err
		}
	}
	for _, option := range options {
		if ClassifyOption(option.ID) != ClassI {
			return nil, fmt.Errorf("%w: option %d is not Class I", ErrInvalidOptionClass, option.ID)
		}
	}
	encodedOptions, err := marshalOptions(options)
	if err != nil {
		return nil, fmt.Errorf("marshal OSCORE Class I options: %w", err)
	}

	aadArray := make([]byte, 0, 16+len(requestKid)+len(requestPartialIV)+len(encodedOptions))
	aadArray = appendCBORHead(aadArray, 4, 5)
	aadArray = appendCBORHead(aadArray, 0, 1)
	aadArray = appendCBORHead(aadArray, 4, 1)
	aadArray = appendCBORHead(aadArray, 0, uint64(algorithm))
	aadArray = appendCBORBytes(aadArray, requestKid)
	aadArray = appendCBORBytes(aadArray, requestPartialIV)
	aadArray = appendCBORBytes(aadArray, encodedOptions)

	encStructure := make([]byte, 0, len(aadArray)+14)
	encStructure = appendCBORHead(encStructure, 4, 3)
	encStructure = appendCBORHead(encStructure, 3, 8)
	encStructure = append(encStructure, "Encrypt0"...)
	encStructure = append(encStructure, 0x40)
	encStructure = appendCBORBytes(encStructure, aadArray)
	return encStructure, nil
}
