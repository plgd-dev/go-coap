package client

import (
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
)

// RegisterRequestHandler registers a response handler and returns its response
// channel and cleanup function. Cleanup takes the request error and returns it
// with recovery instructions if tokenless requests become blocked.
// Errors marked RequestNotSentError leave tokenless requests usable.
func RegisterRequestHandler[C responsewriter.Client](
	token message.Token,
	tokenHandlers *coapSync.Map[uint64, func(*responsewriter.ResponseWriter[C], *pool.Message)],
	tokenlessHandler *TokenlessHandler[func(*responsewriter.ResponseWriter[C], *pool.Message)],
) (<-chan *pool.Message, func(error) error, error) {
	responses := make(chan *pool.Message, 1)
	handler := func(_ *responsewriter.ResponseWriter[C], response *pool.Message) {
		response.Hijack()
		select {
		case responses <- response:
		default:
		}
	}
	if len(token) == 0 {
		remove, err := tokenlessHandler.Register(handler)
		if err != nil {
			return nil, nil, err
		}
		return responses, func(requestErr error) error {
			var notSent *RequestNotSentError
			block := requestErr != nil && !errors.As(requestErr, &notSent)
			remove(!block)
			if block {
				return errors.Join(requestErr, ErrTokenlessRequestsBlocked)
			}
			return requestErr
		}, nil
	}
	if _, loaded := tokenHandlers.LoadOrStore(token.Hash(), handler); loaded {
		return nil, nil, fmt.Errorf("cannot add token(%v) handler: %w", token, coapErrors.ErrKeyAlreadyExists)
	}
	return responses, func(requestErr error) error {
		_, _ = tokenHandlers.LoadAndDelete(token.Hash())
		return requestErr
	}, nil
}
