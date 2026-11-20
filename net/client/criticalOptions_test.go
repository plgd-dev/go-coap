package client

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/stretchr/testify/require"
)

func TestGetUnknownCriticalOptions(t *testing.T) {
	options := message.Options{
		{ID: message.URIPath, Value: []byte("store")},
		{ID: 12, Value: []byte{0x01}},
		{ID: 13, Value: []byte{0x01}},
		{ID: 13, Value: []byte{0x02}},
		{ID: 99, Value: []byte{0x01}},
		{ID: 100, Value: []byte{0x01}},
	}

	unknownCriticalOpts := GetUnknownCriticalOptions(options)
	require.Equal(t, []message.OptionID{13, 99}, unknownCriticalOpts)
}

func TestGetUnknownCriticalOptionsIllegalLength(t *testing.T) {
	options := message.Options{
		{ID: message.IfNoneMatch, Value: []byte{0x00}},
		{ID: message.URIPath, Value: make([]byte, 256)},
		{ID: message.ETag, Value: make([]byte, 9)},
	}

	unknownCriticalOpts := GetUnknownCriticalOptions(options)
	require.Equal(t, []message.OptionID{message.IfNoneMatch, message.URIPath}, unknownCriticalOpts)
}

func TestOSCOREIsRejectedUntilProtectionProcessingIsEnabled(t *testing.T) {
	require.Equal(t,
		[]message.OptionID{message.OSCORE},
		GetUnknownCriticalOptions(message.Options{{ID: message.OSCORE, Value: []byte{0x01}}}),
	)
}

func TestGetUnknownCriticalOptionsRepeated(t *testing.T) {
	options := message.Options{
		{ID: message.IfMatch, Value: []byte{0x01}},
		{ID: message.IfMatch, Value: []byte{0x02}},
		{ID: message.URIHost, Value: []byte("a")},
		{ID: message.URIHost, Value: []byte("b")},
		{ID: message.URIPath, Value: []byte("a")},
		{ID: message.URIPath, Value: []byte("b")},
		{ID: message.URIQuery, Value: []byte("a")},
		{ID: message.URIQuery, Value: []byte("b")},
		{ID: message.Accept, Value: []byte{0x00}},
		{ID: message.Accept, Value: []byte{0x00}},
		{ID: message.Accept, Value: []byte{0x00}},
		{ID: message.Block1, Value: []byte{0x0e}},
		{ID: message.Block1, Value: []byte{0x16}},
		{ID: message.QBlock2, Value: []byte{0x06}},
		{ID: message.QBlock2, Value: []byte{0x16}},
	}

	unknownCriticalOpts := GetUnknownCriticalOptionsWithQBlock(options, true)
	require.Equal(t, []message.OptionID{message.URIHost, message.Accept, message.Block1}, unknownCriticalOpts)
}

func TestFormatUnknownCriticalOptionsDiagnostic(t *testing.T) {
	diagnostic := FormatUnknownCriticalOptionsDiagnostic([]message.OptionID{13, 99})
	require.Equal(t, "unknown critical option(s): 13,99", diagnostic)
}
