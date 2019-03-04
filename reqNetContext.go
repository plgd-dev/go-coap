package coap

import (
	"context"
	"time"

	"github.com/LK4D4/joincontext"
)

type reqNetContext struct {
	ctx context.Context
}

func (c *reqNetContext) Deadline() (deadline time.Time, ok bool) {
	return c.ctx.Deadline()
}

func (c *reqNetContext) Done() <-chan struct{} {
	return c.ctx.Done()
}

func (c *reqNetContext) Err() error {
	return c.ctx.Err()
}

func (c *reqNetContext) Value(key interface{}) interface{} {
	return c.ctx.Value(key)
}

func newReqNetContext(reqCtx context.Context, netCtx context.Context) (*reqNetContext, context.CancelFunc) {
	if reqCtx == nil || netCtx == nil {
		panic("contexts cannot be nil")
	}
	ctx, cancel := joincontext.Join(reqCtx, netCtx)
	return &reqNetContext{ctx}, cancel
}
