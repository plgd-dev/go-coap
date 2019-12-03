package coap

import (
	"time"
)

type shutdownContext struct {
	doneChan <-chan struct{}
}

func newShutdownWithContext(doneChan <-chan struct{}) *shutdownContext {
	return &shutdownContext{doneChan: doneChan}
}

func (ctx *shutdownContext) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (ctx *shutdownContext) Done() <-chan struct{} {
	return ctx.doneChan
}

func (ctx *shutdownContext) Err() error {
	return ErrServerClosed
}

func (ctx *shutdownContext) Value(key interface{}) interface{} {
	return nil
}
