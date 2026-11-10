package oscore

import "fmt"

const maxOSCOREOptionLength = 1 + 5 + 1 + 255 + maxIdentifierSize

// OptionValue contains the fields encoded by the RFC 8613 OSCORE option.
// Presence booleans distinguish an absent field from a present empty value.
type OptionValue struct {
	PartialIV    []byte
	IDContext    []byte
	SenderID     []byte
	IDContextSet bool
	SenderIDSet  bool
}

// MarshalBinary encodes an OSCORE option value.
func (o OptionValue) MarshalBinary() ([]byte, error) {
	if len(o.PartialIV) > 0 {
		if _, err := decodePartialIV(o.PartialIV); err != nil {
			return nil, err
		}
	}
	if o.IDContextSet && len(o.IDContext) > 255 {
		return nil, fmt.Errorf("%w: ID Context exceeds 255 bytes", ErrInvalidContext)
	}
	if o.SenderIDSet && len(o.SenderID) > maxIdentifierSize {
		return nil, fmt.Errorf("%w: sender ID exceeds %d bytes", ErrInvalidContext, maxIdentifierSize)
	}
	flags := byte(len(o.PartialIV) & 0xff)
	if o.SenderIDSet {
		flags |= 0x08
	}
	if o.IDContextSet {
		flags |= 0x10
	}
	if flags == 0 {
		return []byte{}, nil
	}

	value := make([]byte, 0, 1+len(o.PartialIV)+len(o.IDContext)+len(o.SenderID)+1)
	value = append(value, flags)
	value = append(value, o.PartialIV...)
	if o.IDContextSet {
		value = append(value, byte(len(o.IDContext)&0xff))
		value = append(value, o.IDContext...)
	}
	if o.SenderIDSet {
		value = append(value, o.SenderID...)
	}
	if len(value) > maxOSCOREOptionLength {
		return nil, fmt.Errorf("%w: OSCORE option exceeds %d bytes", ErrInvalidContext, maxOSCOREOptionLength)
	}
	return value, nil
}

// UnmarshalOption decodes the value of an RFC 8613 OSCORE option.
func UnmarshalOption(value []byte) (OptionValue, error) {
	if len(value) == 0 {
		return OptionValue{}, nil
	}
	if len(value) > maxOSCOREOptionLength {
		return OptionValue{}, fmt.Errorf("%w: OSCORE option exceeds %d bytes", ErrInvalidContext, maxOSCOREOptionLength)
	}
	flags := value[0]
	if flags&0xe0 != 0 || flags&0x1f == 0 {
		return OptionValue{}, fmt.Errorf("%w: invalid OSCORE option flags", ErrInvalidContext)
	}
	pivLen := int(flags & 0x07)
	if pivLen > 5 || len(value) < 1+pivLen {
		return OptionValue{}, fmt.Errorf("%w: invalid Partial IV length", ErrInvalidPartialIV)
	}
	decoded := OptionValue{
		PartialIV:    append([]byte(nil), value[1:1+pivLen]...),
		IDContextSet: flags&0x10 != 0,
		SenderIDSet:  flags&0x08 != 0,
	}
	if pivLen > 0 {
		if _, err := decodePartialIV(decoded.PartialIV); err != nil {
			return OptionValue{}, err
		}
	}

	offset, err := decodeOptionFields(value, 1+pivLen, &decoded)
	if err != nil {
		return OptionValue{}, err
	}
	if offset != len(value) {
		return OptionValue{}, fmt.Errorf("%w: trailing bytes in OSCORE option", ErrInvalidContext)
	}
	return decoded, nil
}

func decodeOptionFields(value []byte, offset int, decoded *OptionValue) (int, error) {
	if decoded.IDContextSet {
		if offset >= len(value) {
			return offset, fmt.Errorf("%w: missing ID Context length", ErrInvalidContext)
		}
		contextLen := int(value[offset])
		offset++
		if len(value)-offset < contextLen {
			return offset, fmt.Errorf("%w: truncated ID Context", ErrInvalidContext)
		}
		decoded.IDContext = append([]byte{}, value[offset:offset+contextLen]...)
		offset += contextLen
	}
	if decoded.SenderIDSet {
		if len(value)-offset > maxIdentifierSize {
			return offset, fmt.Errorf("%w: sender ID exceeds %d bytes", ErrInvalidContext, maxIdentifierSize)
		}
		decoded.SenderID = append([]byte{}, value[offset:]...)
		offset = len(value)
	}
	return offset, nil
}
