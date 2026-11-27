package writecontext

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrWriteTimeout identifies expiration of the configured server write timeout.
var ErrWriteTimeout = fmt.Errorf("server write timeout: %w", context.DeadlineExceeded)

type requestDeadline struct {
	context.Context
	request context.Context
}

func (c requestDeadline) Deadline() (time.Time, bool) {
	requestDeadline, hasRequest := c.request.Deadline()
	deadline, hasDeadline := c.Context.Deadline()
	if hasRequest && (!hasDeadline || requestDeadline.Before(deadline)) {
		return requestDeadline, true
	}
	return deadline, hasDeadline
}

// New returns a context canceled with the request, session close, or timeout.
func New(ctx, closeCtx context.Context, timeout time.Duration) (context.Context, func()) {
	request := ctx
	ctx, cancel := context.WithCancelCause(context.WithoutCancel(request))
	cancelRequest := func() {
		if closeCtx.Err() != nil {
			cancel(net.ErrClosed)
		} else {
			cancel(context.Cause(request))
		}
	}
	stopRequest := context.AfterFunc(request, cancelRequest)
	if closeCtx.Err() != nil {
		cancel(net.ErrClosed)
	} else if request.Err() != nil {
		cancelRequest()
	}
	stop := context.AfterFunc(closeCtx, func() { cancel(net.ErrClosed) })
	if timeout > 0 {
		timedCtx, timedCancel := context.WithTimeoutCause(ctx, timeout, ErrWriteTimeout)
		return requestDeadline{Context: timedCtx, request: request}, func() {
			stopRequest()
			stop()
			timedCancel()
			cancel(context.Canceled)
		}
	}
	return requestDeadline{Context: ctx, request: request}, func() {
		stopRequest()
		stop()
		cancel(context.Canceled)
	}
}

// Error joins a context's cancellation and cause, preserving both identities.
func Error(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(err, context.Cause(ctx))
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		<-ctx.Done()
		return errors.Join(ctx.Err(), context.Cause(ctx))
	}
	return nil
}
