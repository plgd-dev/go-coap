package qblock

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestDecoderPreservesInvalidQBlockLength(t *testing.T) {
	raw := []byte{0x40, 0x01, 0x00, 0x01, 0xd4, 0x06, 0, 0, 0, 0}
	m := message.Message{Options: make(message.Options, 0, 4)}
	_, err := (Decoder{}).Decode(raw, &m)
	require.NoError(t, err)
	require.True(t, m.Options.HasOption(message.QBlock1))
	require.Error(t, ValidateOptions(m.Options, true))
}

func TestDecoderPoolRetryAndReuse(t *testing.T) {
	// Seventeen options force the pooled decoder to grow its option capacity.
	raw := append([]byte{0x40, 0x01, 0, 1, 0xd0, 0x06}, make([]byte, 16)...)
	p := pool.New(1, 1024)
	m := p.AcquireMessage(context.Background())
	_, err := m.UnmarshalWithDecoder(Decoder{}, raw)
	require.NoError(t, err)
	require.Len(t, m.Options(), 17)
	p.ReleaseMessage(m)

	m = p.AcquireMessage(context.Background())
	_, err = m.UnmarshalWithDecoder(Decoder{}, raw)
	require.NoError(t, err)
	require.Len(t, m.Options(), 17)
	p.ReleaseMessage(m)
}

func TestValidateOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    message.Options
		request bool
		valid   bool
	}{
		{"empty Q values", message.Options{{ID: message.QBlock1}, {ID: message.QBlock2}}, true, true},
		{"long Q value", message.Options{{ID: message.QBlock1, Value: make([]byte, 4)}}, true, false},
		{"long request tag", message.Options{{ID: message.RequestTag, Value: make([]byte, 9)}}, true, false},
		{"repeated Q1", message.Options{{ID: message.QBlock1}, {ID: message.QBlock1}}, true, false},
		{"repeated Q2 request", message.Options{{ID: message.QBlock2}, {ID: message.QBlock2}}, true, true},
		{"repeated Q2 response", message.Options{{ID: message.QBlock2}, {ID: message.QBlock2}}, false, false},
		{"repeated tags", message.Options{{ID: message.RequestTag}, {ID: message.RequestTag}}, true, true},
		{"classic only", message.Options{{ID: message.Block1}, {ID: message.Block2}}, true, true},
		{"Q1 and classic 1", message.Options{{ID: message.QBlock1}, {ID: message.Block1}}, true, false},
		{"Q1 and classic 2", message.Options{{ID: message.QBlock1}, {ID: message.Block2}}, true, false},
		{"Q2 and classic 1", message.Options{{ID: message.QBlock2}, {ID: message.Block1}}, true, false},
		{"Q2 and classic 2", message.Options{{ID: message.QBlock2}, {ID: message.Block2}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOptions(tt.opts, tt.request)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestDecoderRejectsTruncatedOption(t *testing.T) {
	m := message.Message{Options: make(message.Options, 0, 4)}
	_, err := (Decoder{}).Decode([]byte{0x40, 0x01, 0, 1, 0xd0}, &m)
	require.Error(t, err)
}
