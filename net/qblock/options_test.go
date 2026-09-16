package qblock

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestDecoderPreservesInvalidQBlockLength(t *testing.T) {
	raw := []byte{0x40, 1, 0, 1, 0xd4, 6, 0, 0, 0, 0}
	m := message.Message{Options: make(message.Options, 0, 4)}
	_, err := coder.DefaultCoder.Decode(raw, &m)
	require.NoError(t, err)
	require.Empty(t, m.Options)
	_, err = (Decoder{}).Decode(raw, &m)
	require.NoError(t, err)
	require.True(t, m.Options.HasOption(message.QBlock1))
	require.Error(t, ValidateOptions(m.Options, true))
}

func TestValidateOptions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		opts             message.Options
		request, invalid bool
	}{
		{"no options", nil, true, false},
		{"zero", message.Options{{ID: message.QBlock1}}, true, false},
		{"long", message.Options{{ID: message.QBlock2, Value: make([]byte, 4)}}, true, true},
		{"tag", message.Options{{ID: message.RequestTag, Value: make([]byte, 9)}}, true, true},
		{"duplicate q1", message.Options{{ID: message.QBlock1}, {ID: message.QBlock1}}, true, true},
		{"repair", message.Options{{ID: message.QBlock2}, {ID: message.QBlock2}}, true, false},
		{"response", message.Options{{ID: message.QBlock2}, {ID: message.QBlock2}}, false, true},
		{"tags", message.Options{{ID: message.RequestTag}, {ID: message.RequestTag}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOptions(tc.opts, tc.request)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	for _, q := range []message.OptionID{message.QBlock1, message.QBlock2} {
		for _, classic := range []message.OptionID{message.Block1, message.Block2} {
			require.Error(t, ValidateOptions(message.Options{{ID: q}, {ID: classic}}, true))
		}
	}
}

func TestDecoderPoolGrowth(t *testing.T) {
	// 20 repeated zero-length QBlock2 options force pooled option capacity growth.
	raw := append([]byte{0x40, 1, 0, 1, 0xd0, 18}, make([]byte, 19)...)
	p := pool.New(1, 1)
	for i := 0; i < 2; i++ {
		m := p.AcquireMessage(context.Background())
		_, err := m.UnmarshalWithDecoder(Decoder{}, raw)
		require.NoError(t, err)
		require.Len(t, m.Options(), 20)
		p.ReleaseMessage(m)
	}
	for _, raw := range [][]byte{{0x40, 1, 0, 1, 0xd0}, {0x40, 1, 0, 1, 0xd4, 6, 0}} {
		m := message.Message{Options: make(message.Options, 0, 4)}
		_, err := (Decoder{}).Decode(raw, &m)
		require.Error(t, err)
	}
}

func TestDecoderPreservesMalformedClassicWhenMixed(t *testing.T) {
	raw := []byte{0x40, 1, 0, 1, 0xd0, 6, 0x44, 0, 0, 0, 0}
	m := message.Message{Options: make(message.Options, 0, 4)}
	_, err := (Decoder{}).Decode(raw, &m)
	require.NoError(t, err)
	require.True(t, m.Options.HasOption(message.Block2))
	require.Error(t, ValidateOptions(m.Options, true))
	// Ordinary malformed classic options keep existing permissive behavior.
	raw = []byte{0x40, 1, 0, 1, 0xd4, 10, 0, 0, 0, 0}
	m.Options = m.Options[:0]
	_, err = (Decoder{}).Decode(raw, &m)
	require.NoError(t, err)
	require.Empty(t, m.Options)
}

func TestMixedOptionsTakePrecedence(t *testing.T) {
	opts := message.Options{{ID: message.QBlock1, Value: make([]byte, 4)}, {ID: message.Block2}}
	require.ErrorIs(t, ValidateOptions(opts, true), ErrMixedOptions)
}
