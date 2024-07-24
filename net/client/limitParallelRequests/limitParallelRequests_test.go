package limitparallelrequests

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func newReq(ctx context.Context, t *testing.T) *pool.Message {
	m := pool.NewMessage(ctx)
	m.SetCode(codes.GET)
	err := m.SetPath("/a")
	require.NoError(t, err)
	return m
}

type mockClient struct {
	num atomic.Int32
}

func (c *mockClient) do(*pool.Message) (*pool.Message, error) {
	c.num.Inc()
	return nil, errors.New("not implemented")
}

func (c *mockClient) doObserve(*pool.Message, func(req *pool.Message)) (Observation, error) {
	c.num.Inc()
	return nil, errors.New("not implemented")
}

func TestLimitParallelRequestsDo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	type args struct {
		limit         int64
		endpointLimit int64
		req           []*pool.Message
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "limit 1 endpointLimit 1",
			args: args{
				limit:         1,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit n endpointLimit 1",
			args: args{
				limit:         0,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit 1 endpointLimit n",
			args: args{
				limit:         1,
				endpointLimit: 0,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit n endpointLimit n",
			args: args{
				limit:         0,
				endpointLimit: 0,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "context canceled",
			args: args{
				limit:         1,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mockedClient mockClient
			c := New(tt.args.endpointLimit, tt.args.limit, mockedClient.do, mockedClient.doObserve)
			var wg sync.WaitGroup
			wg.Add(len(tt.args.req))
			for idx, req := range tt.args.req {
				req.SetMessageID(int32(idx))
				go func(r *pool.Message) {
					defer wg.Done()
					_, err := c.Do(r)
					assert.Error(t, err)
				}(req)
			}
			wg.Wait()
			require.GreaterOrEqual(t, len(tt.args.req), int(mockedClient.num.Load()))
			require.Equal(t, 0, c.endpointQueues.Length())
		})
	}
}

func TestLimitParallelRequestsDoObserve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	type args struct {
		limit         int64
		endpointLimit int64
		req           []*pool.Message
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "limit 1 endpointLimit 1",
			args: args{
				limit:         1,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit n endpointLimit 1",
			args: args{
				limit:         0,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit 1 endpointLimit n",
			args: args{
				limit:         1,
				endpointLimit: 0,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "limit n endpointLimit n",
			args: args{
				limit:         0,
				endpointLimit: 0,
				req: []*pool.Message{
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(ctx, t), newReq(ctx, t),
				},
			},
		},
		{
			name: "context canceled",
			args: args{
				limit:         1,
				endpointLimit: 1,
				req: []*pool.Message{
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
					newReq(ctx, t), newReq(canceledCtx, t), newReq(ctx, t),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mockedClient mockClient
			c := New(tt.args.endpointLimit, tt.args.limit, mockedClient.do, mockedClient.doObserve)
			var wg sync.WaitGroup
			wg.Add(len(tt.args.req))
			for idx, req := range tt.args.req {
				req.SetMessageID(int32(idx))
				go func(r *pool.Message) {
					defer wg.Done()
					_, err := c.DoObserve(r, func(*pool.Message) {
						// do nothing
					})
					assert.Error(t, err)
				}(req)
			}
			wg.Wait()
			require.GreaterOrEqual(t, len(tt.args.req), int(mockedClient.num.Load()))
			require.Equal(t, 0, c.endpointQueues.Length())
		})
	}
}
