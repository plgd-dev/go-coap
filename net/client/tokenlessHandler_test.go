package client

import (
	"testing"

	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestTokenlessHandler(t *testing.T) {
	var handler TokenlessHandler[string]
	_, loaded := handler.Load(false)
	require.False(t, loaded)

	removeFirst, err := handler.Register("first")
	require.NoError(t, err)
	defer removeFirst(true)
	_, err = handler.Register("duplicate")
	require.ErrorIs(t, err, coapErrors.ErrKeyAlreadyExists)

	value, loaded := handler.Load(false)
	require.True(t, loaded)
	require.Equal(t, "first", value)
	value, loaded = handler.Load(true)
	require.True(t, loaded)
	require.Equal(t, "first", value)
	_, loaded = handler.Load(false)
	require.False(t, loaded)

	_, err = handler.Register("still pending")
	require.ErrorIs(t, err, coapErrors.ErrKeyAlreadyExists)
	removeFirst(true)
	removeSecond, err := handler.Register("second")
	require.NoError(t, err)
	defer removeSecond(true)
	removeFirst(false)
	value, loaded = handler.Load(false)
	require.True(t, loaded)
	require.Equal(t, "second", value)

	removeSecond(true)
	_, loaded = handler.Load(false)
	require.False(t, loaded)
}

func TestTokenlessHandlerBlockedAfterFailure(t *testing.T) {
	var handler TokenlessHandler[string]
	remove, err := handler.Register("timed out")
	require.NoError(t, err)
	remove(false)
	remove(true)
	_, loaded := handler.Load(false)
	require.False(t, loaded)
	_, err = handler.Register("next")
	require.ErrorIs(t, err, ErrTokenlessRequestsBlocked)
	require.Contains(t, err.Error(), "recreate the connection")
}
