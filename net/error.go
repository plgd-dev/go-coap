package net

import (
	"context"
	"errors"
	"io"
	"strings"
)

var ErrListenerIsClosed = io.EOF
var ErrConnectionIsClosed = io.EOF
var ErrWriteInterrupted = errors.New("only part data was written to socket")

func IsCancelOrCloseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") {
		// this error was produced by cancellation context or closing connection.
		return true
	}
	return false
}
