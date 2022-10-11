package noresponse

import (
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func TestNoResponse2XXCodes(t *testing.T) {
	codes := decodeNoResponseOption(2)
	exp := resp2XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponse4XXCodes(t *testing.T) {
	codes := decodeNoResponseOption(8)
	exp := resp4XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponse5XXCodes(t *testing.T) {
	codes := decodeNoResponseOption(16)
	exp := resp5XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponseCombinationXXCodes(t *testing.T) {
	codes := decodeNoResponseOption(18)
	exp := resp2XXCodes
	exp = append(exp, resp5XXCodes...)
	require.Equal(t, exp, codes)
}

func TestNoResponseAllCodes(t *testing.T) {
	allCodes := decodeNoResponseOption(0)
	exp := []codes.Code(nil)
	require.Equal(t, exp, allCodes)
}

func TestNoResponseBehaviour(t *testing.T) {
	err := IsNoResponseCode(codes.Content, 2)
	require.Error(t, err)
}
