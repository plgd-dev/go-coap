package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
	"github.com/stretchr/testify/require"
)

func TestRequestNotSentAllowsTokenlessRetry(t *testing.T) {
	type handlerFunc = func(*responsewriter.ResponseWriter[responsewriter.Client], *pool.Message)
	handlers := coapSync.NewMap[uint64, handlerFunc]()
	var tokenless TokenlessHandler[handlerFunc]
	_, finish, err := RegisterRequestHandler(nil, handlers, &tokenless)
	require.NoError(t, err)
	notSent := fmt.Errorf("write failed: %w", &RequestNotSentError{Err: context.Canceled})
	failure := finish(notSent)
	require.ErrorIs(t, failure, context.Canceled)
	require.NotErrorIs(t, failure, ErrTokenlessRequestsBlocked)
	require.Equal(t, notSent.Error(), failure.Error())

	_, finishNext, err := RegisterRequestHandler(nil, handlers, &tokenless)
	require.NoError(t, err)
	require.NoError(t, finishNext(nil))
}
