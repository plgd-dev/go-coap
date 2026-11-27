package net

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
)

var (
	ErrListenerIsClosed   = io.EOF
	ErrConnectionIsClosed = io.EOF
	ErrWriteInterrupted   = errors.New("only part data was written to socket")
	// ErrWriteTimeout identifies expiration of the configured server write timeout.
	ErrWriteTimeout = writecontext.ErrWriteTimeout
)

func IsCancelOrCloseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		// this error was produced by cancellation context or closing connection.
		return true
	}
	return false
}
