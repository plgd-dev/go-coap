package qblock

import (
	"fmt"
	"sync"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

var (
	rawOptionDefs     map[message.OptionID]message.OptionDef
	rawOptionDefsOnce sync.Once
)

func qOptionDefs() map[message.OptionID]message.OptionDef {
	rawOptionDefsOnce.Do(func() {
		rawOptionDefs = make(map[message.OptionID]message.OptionDef, len(message.CoapOptionDefs))
		for id, def := range message.CoapOptionDefs {
			rawOptionDefs[id] = def
		}
		for _, id := range []message.OptionID{message.QBlock1, message.QBlock2, message.RequestTag} {
			rawOptionDefs[id] = message.OptionDef{ValueFormat: message.ValueOpaque, MaxLen: ^uint32(0)}
		}
	})
	return rawOptionDefs
}

// Decoder preserves raw Q-Block option values for validation before dispatch.
type Decoder struct{}

func (Decoder) Decode(data []byte, m *message.Message) (int, error) {
	return coder.DefaultCoder.DecodeWithOptionDefs(data, m, qOptionDefs())
}

// ValidateOptions checks Q-Block option lengths, repetition, and classic/Q mixing.
func ValidateOptions(opts message.Options, request bool) error {
	var q1, q2, classic int
	for _, opt := range opts {
		switch opt.ID {
		case message.QBlock1:
			q1++
			if len(opt.Value) > 3 {
				return fmt.Errorf("invalid Q-Block1 length %d", len(opt.Value))
			}
		case message.QBlock2:
			q2++
			if len(opt.Value) > 3 {
				return fmt.Errorf("invalid Q-Block2 length %d", len(opt.Value))
			}
		case message.RequestTag:
			if len(opt.Value) > 8 {
				return fmt.Errorf("invalid Request-Tag length %d", len(opt.Value))
			}
		case message.Block1, message.Block2:
			classic++
		}
	}
	if q1 > 1 || (!request && q2 > 1) {
		return fmt.Errorf("duplicate Q-Block option")
	}
	if classic > 0 && (q1 > 0 || q2 > 0) {
		return fmt.Errorf("mixed classic and Q-Block options")
	}
	return nil
}
