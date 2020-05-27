package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetToken(t *testing.T) {
	token, err := GetToken()
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotEmpty(t, token.String())

}
