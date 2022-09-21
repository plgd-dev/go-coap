package limitparallelrequests

import (
	"context"
	"math"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"golang.org/x/sync/semaphore"
)

type (
	DoFunc        = func(req *pool.Message) (*pool.Message, error)
	DoObserveFunc = func(req *pool.Message, observeFunc func(req *pool.Message), opts ...message.Option) (Observation, error)
)

type Observation = interface {
	Cancel(ctx context.Context) error
	Canceled() bool
}

type LimitParallelRequests struct {
	sem       *semaphore.Weighted
	do        DoFunc
	doObserve DoObserveFunc
}

func New(limit int64, do DoFunc, doObserve DoObserveFunc) *LimitParallelRequests {
	if limit <= 0 {
		limit = math.MaxInt64
	}
	return &LimitParallelRequests{
		sem:       semaphore.NewWeighted(limit),
		do:        do,
		doObserve: doObserve,
	}
}

func (c *LimitParallelRequests) Do(req *pool.Message) (*pool.Message, error) {
	err := c.sem.Acquire(req.Context(), 1)
	if err != nil {
		return nil, err
	}
	defer c.sem.Release(1)
	return c.do(req)
}

func (c *LimitParallelRequests) DoObserve(req *pool.Message, observeFunc func(req *pool.Message), opts ...message.Option) (Observation, error) {
	err := c.sem.Acquire(req.Context(), 1)
	if err != nil {
		return nil, err
	}
	defer c.sem.Release(1)
	return c.doObserve(req, observeFunc, opts...)
}
