// Package qblock provides foundations for RFC 9177 datagram transfers.
package qblock

import (
	"errors"
	"fmt"
	"maps"
	"math"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// ErrMixedOptions indicates classic and Q-Block options in the same packet.
var ErrMixedOptions = errors.New("mixed classic and Q-Block options")

var rawOptionDefs = func() map[message.OptionID]message.OptionDef {
	defs := maps.Clone(message.CoapOptionDefs)
	for _, id := range []message.OptionID{message.QBlock1, message.QBlock2} {
		defs[id] = message.OptionDef{ValueFormat: message.ValueOpaque, MaxLen: math.MaxUint32}
	}
	return defs
}()

var mixedOptionDefs = func() map[message.OptionID]message.OptionDef {
	defs := maps.Clone(rawOptionDefs)
	for _, id := range []message.OptionID{message.Block1, message.Block2, message.RequestTag} {
		defs[id] = message.OptionDef{ValueFormat: message.ValueOpaque, MaxLen: math.MaxUint32}
	}
	return defs
}()

// Decoder preserves Q options for validation, including illegal value lengths.
// It is safe for concurrent use and does not change the default decoder.
type Decoder struct{}

// Decode implements pool.Decoder.
func (Decoder) Decode(data []byte, m *message.Message) (int, error) {
	n, err := coder.DefaultCoder.DecodeWithOptionDefs(data, m, rawOptionDefs)
	if err != nil || (!m.Options.HasOption(message.QBlock1) && !m.Options.HasOption(message.QBlock2)) {
		return n, err
	}
	// Retain malformed classic options too: they still make a Q packet mixed.
	// Re-decode only Q packets, preserving ordinary decoder behavior.
	m.Options = m.Options[:0]
	return coder.DefaultCoder.DecodeWithOptionDefs(data, m, mixedOptionDefs)
}

// ValidateOptions validates Q-related lengths, multiplicity and classic/Q mixing.
// Repeated QBlock2 request semantics are checked by the transfer engine.
func ValidateOptions(opts message.Options, request bool) error {
	var hasQ, hasClassic bool
	for _, opt := range opts {
		switch opt.ID {
		case message.QBlock1, message.QBlock2:
			hasQ = true
		case message.Block1, message.Block2:
			hasClassic = true
		}
	}
	if hasQ && hasClassic {
		return ErrMixedOptions
	}
	var q1, q2 int
	for _, opt := range opts {
		switch opt.ID {
		case message.QBlock1, message.QBlock2:
			if len(opt.Value) > 3 {
				return fmt.Errorf("%s: %w", opt.ID, message.ErrInvalidValueLength)
			}
			if opt.ID == message.QBlock1 {
				q1++
			} else {
				q2++
			}
		case message.RequestTag:
			if len(opt.Value) > 8 {
				return fmt.Errorf("RequestTag: %w", message.ErrInvalidValueLength)
			}
		}
	}
	if q1 > 1 || (!request && q2 > 1) {
		return message.ErrOptionDuplicate
	}
	return nil
}
