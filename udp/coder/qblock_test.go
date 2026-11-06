package coder

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func TestQBlockWireOptions(t *testing.T) {
	tests := []struct {
		name string
		opts message.Options
	}{
		{
			name: "explicit-zero",
			opts: message.Options{{ID: message.QBlock2, Value: []byte{}}},
		},
		{
			name: "absent",
		},
		{
			name: "repeated-recovery",
			opts: message.Options{
				{ID: message.QBlock2, Value: []byte{0x16}},
				{ID: message.QBlock2, Value: []byte{0x36}},
				{ID: message.QBlock2, Value: []byte{0x56}},
			},
		},
		{
			name: "request",
			opts: message.Options{
				{ID: message.QBlock1, Value: []byte{0xff, 0xff, 0xfe}},
				{ID: message.Size1, Value: []byte{0x40, 0, 0, 0}},
				{ID: message.RequestTag, Value: []byte{1, 2, 3, 4}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := message.Message{
				Code: codes.GET, Type: message.Confirmable, MessageID: 1,
				Token: message.Token{1, 2, 3, 4}, Options: tt.opts,
			}
			size, err := DefaultCoder.Size(in)
			require.NoError(t, err)
			data := make([]byte, size)
			n, err := DefaultCoder.Encode(in, data)
			require.NoError(t, err)
			require.Equal(t, size, n)
			var out message.Message
			if len(tt.opts) > 0 {
				out.Options = make(message.Options, 0, len(tt.opts))
			}
			n, err = DefaultCoder.Decode(data, &out)
			require.NoError(t, err)
			require.Equal(t, size, n)
			require.Equal(t, in.Options, out.Options)
			require.Equal(t, in.Token, out.Token)
			require.Equal(t, in.Code, out.Code)
		})
	}
}
