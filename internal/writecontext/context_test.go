package writecontext

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewCauses(t *testing.T) {
	tests := []struct {
		name        string
		timeout     time.Duration
		prepare     func(context.CancelFunc, context.CancelFunc)
		wantErr     error
		wantCause   error
		wantTimeout bool
	}{
		{name: "timeout", timeout: 200 * time.Millisecond, wantErr: context.DeadlineExceeded, wantCause: ErrWriteTimeout, wantTimeout: true},
		{name: "request canceled", prepare: func(request, _ context.CancelFunc) { request() }, wantErr: context.Canceled, wantCause: context.Canceled},
		{name: "session closed", prepare: func(_, closeSession context.CancelFunc) { closeSession() }, wantErr: context.Canceled, wantCause: net.ErrClosed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			closeCtx, cancelClose := context.WithCancel(context.Background())
			defer cancelClose()
			ctx, cancel := New(request, closeCtx, tt.timeout)
			defer cancel()
			if tt.prepare != nil {
				tt.prepare(cancelRequest, cancelClose)
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("write context did not finish")
			}
			require.ErrorIs(t, ctx.Err(), tt.wantErr)
			require.ErrorIs(t, context.Cause(ctx), tt.wantCause)
			require.ErrorIs(t, Error(ctx), tt.wantErr)
			require.ErrorIs(t, Error(ctx), tt.wantCause)
			if !tt.wantTimeout {
				require.NotErrorIs(t, Error(ctx), ErrWriteTimeout)
			}
		})
	}
}

func TestNewPreservesRequestCauseAndValues(t *testing.T) {
	type key struct{}
	request, cancelRequest := context.WithCancelCause(context.WithValue(context.Background(), key{}, "value"))
	defer cancelRequest(nil)
	ctx, cancel := New(request, context.Background(), time.Hour)
	defer cancel()
	require.Equal(t, "value", ctx.Value(key{}))
	cause := errors.New("request aborted")
	cancelRequest(cause)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not propagate")
	}
	require.ErrorIs(t, Error(ctx), context.Canceled)
	require.ErrorIs(t, Error(ctx), cause)
}

func TestNewRequestDeadlineAndDisabledTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			request, cancelRequest := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancelRequest()
			ctx, cancel := New(request, context.Background(), timeout)
			defer cancel()
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			expected, _ := request.Deadline()
			require.Equal(t, expected, deadline)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("request deadline did not cancel write context")
			}
			require.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
			require.NotErrorIs(t, Error(ctx), ErrWriteTimeout)
		})
	}
}

type trackedContext struct {
	context.Context
	active atomic.Int32
	done   chan struct{}
}

func (c *trackedContext) Done() <-chan struct{} { return c.done }

func (c *trackedContext) AfterFunc(f func()) func() bool {
	c.active.Add(1)
	var once sync.Once
	done := func() { once.Do(func() { c.active.Add(-1) }) }
	stop := context.AfterFunc(c.Context, func() {
		done()
		f()
	})
	return func() bool {
		stopped := stop()
		if stopped {
			done()
		}
		return stopped
	}
}

func TestNewCancelUnregistersCallbacksAndCancelsTimerContext(t *testing.T) {
	request := &trackedContext{Context: context.Background(), done: make(chan struct{})}
	closeCtx := &trackedContext{Context: context.Background(), done: make(chan struct{})}
	for range 100 {
		ctx, cancel := New(request, closeCtx, time.Hour)
		require.EqualValues(t, 1, request.active.Load())
		require.EqualValues(t, 1, closeCtx.active.Load())
		cancel()
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.ErrorIs(t, context.Cause(ctx), context.Canceled)
		require.EqualValues(t, 0, request.active.Load())
		require.EqualValues(t, 0, closeCtx.active.Load())
		cancel()
	}
}
