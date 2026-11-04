package client

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/stretchr/testify/require"
)

func TestQBlockCriticalRecognitionRequiresActiveReceiver(t *testing.T) {
	opts := message.Options{
		{ID: message.QBlock1, Value: []byte{0}},
		{ID: message.QBlock2, Value: []byte{0}},
	}
	require.Equal(t, []message.OptionID{message.QBlock1, message.QBlock2}, GetUnknownCriticalOptions(opts))
	require.Empty(t, GetUnknownCriticalOptionsWithQBlock(opts, true))
	opts[0].Value = make([]byte, 4)
	require.Equal(t, []message.OptionID{message.QBlock1}, GetUnknownCriticalOptionsWithQBlock(opts, true))
}
