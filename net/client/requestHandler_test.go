package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
	"github.com/stretchr/testify/require"
)

func TestRegisterRequestHandler(t *testing.T) {
	for _, token := range []message.Token{nil, {1, 2, 3}} {
		t.Run(token.String(), func(t *testing.T) {
			type handlerFunc = func(*responsewriter.ResponseWriter[responsewriter.Client], *pool.Message)
			handlers := coapSync.NewMap[uint64, handlerFunc]()
			var tokenless TokenlessHandler[handlerFunc]
			responses, remove, err := RegisterRequestHandler(token, handlers, &tokenless)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, remove(nil))
			}()

			_, _, err = RegisterRequestHandler(token, handlers, &tokenless)
			require.ErrorIs(t, err, coapErrors.ErrKeyAlreadyExists)

			var handler handlerFunc
			var loaded bool
			if len(token) == 0 {
				handler, loaded = tokenless.Load(false)
			} else {
				handler, loaded = handlers.Load(token.Hash())
			}
			require.True(t, loaded)
			response := pool.NewMessage(context.Background())
			handler(nil, response)
			require.True(t, response.IsHijacked())
			select {
			case got := <-responses:
				require.Same(t, response, got)
			default:
				t.Fatal("response was not delivered")
			}

			require.NoError(t, remove(nil))
			_, loaded = tokenless.Load(false)
			require.False(t, loaded)
			_, loaded = handlers.Load(token.Hash())
			require.False(t, loaded)
			_, removeNext, err := RegisterRequestHandler(token, handlers, &tokenless)
			require.NoError(t, err)
			require.NoError(t, removeNext(nil))
		})
	}
}

func TestRegisterRequestHandlerLateResponse(t *testing.T) {
	type handlerFunc = func(*responsewriter.ResponseWriter[responsewriter.Client], *pool.Message)
	handlers := coapSync.NewMap[uint64, handlerFunc]()
	var tokenless TokenlessHandler[handlerFunc]
	_, finish, err := RegisterRequestHandler(nil, handlers, &tokenless)
	require.NoError(t, err)
	failure := finish(context.DeadlineExceeded)
	require.ErrorIs(t, failure, context.DeadlineExceeded)
	require.ErrorIs(t, failure, ErrTokenlessRequestsBlocked)
	require.Contains(t, failure.Error(), "recreate the connection")

	for _, consume := range []bool{false, true} {
		_, loaded := tokenless.Load(consume)
		require.False(t, loaded, "late responses must not find a handler")
		_, _, err = RegisterRequestHandler(nil, handlers, &tokenless)
		require.ErrorIs(t, err, ErrTokenlessRequestsBlocked)
	}

	token := message.Token{1}
	responses, finishToken, err := RegisterRequestHandler(token, handlers, &tokenless)
	require.NoError(t, err)
	handler, loaded := handlers.Load(token.Hash())
	require.True(t, loaded)
	response := pool.NewMessage(context.Background())
	handler(nil, response)
	require.Same(t, response, <-responses)
	require.NoError(t, finishToken(nil))

	var reconnected TokenlessHandler[handlerFunc]
	_, finishNew, err := RegisterRequestHandler(nil, handlers, &reconnected)
	require.NoError(t, err)
	require.NoError(t, finishNew(nil))
}
