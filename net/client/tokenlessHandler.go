package client

import (
	"errors"
	"fmt"
	"sync"

	coapErrors "github.com/plgd-dev/go-coap/v3/pkg/errors"
)

// ErrTokenlessRequestsBlocked indicates that the connection must be recreated
// before sending another tokenless request.
var ErrTokenlessRequestsBlocked = errors.New("tokenless requests blocked after an unsuccessful request: a late response could be mistaken for a new response; recreate the connection to send tokenless requests again")

// TokenlessHandler tracks the single pending tokenless request on a connection.
// Its zero value is ready to use.
type TokenlessHandler[H any] struct {
	mu       sync.Mutex
	handler  *H
	consumed bool
	blocked  bool
}

// Register installs a handler and returns its cleanup function. Cleanup must be
// called with true only if the request completed successfully; otherwise future
// registrations are permanently blocked.
func (h *TokenlessHandler[H]) Register(handler H) (func(bool), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blocked {
		return nil, ErrTokenlessRequestsBlocked
	}
	if h.handler != nil {
		return nil, fmt.Errorf("cannot add tokenless request handler: %w", coapErrors.ErrKeyAlreadyExists)
	}
	h.handler = &handler
	h.consumed = false
	return func(completed bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.handler == &handler {
			h.handler = nil
			h.blocked = !completed
		}
	}, nil
}

// Load returns the current handler, optionally consuming it. The registration
// remains reserved until cleanup determines whether the request completed.
func (h *TokenlessHandler[H]) Load(remove bool) (H, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handler == nil || h.consumed {
		var zero H
		return zero, false
	}
	handler := *h.handler
	if remove {
		h.consumed = true
	}
	return handler, true
}
