package client

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	netclient "github.com/plgd-dev/go-coap/v3/net/client"
	"github.com/stretchr/testify/require"
)

type qRouteContextSession struct {
	qContextSession
}

func (s qRouteContextSession) Done() <-chan struct{} { return s.Context().Done() }

func newQRouteContextConn(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cc := &Conn{session: qRouteContextSession{qContextSession{ctx: ctx}}, messagePool: pool.New(0, 0)}
	cc.Client = netclient.New[*Conn](cc, nil, message.GetToken, nil)
	cc.receivedMessageReader = netclient.NewReceivedMessageReader(cc, 1)
	return cc
}

func TestQBlockExchangeCancellationReleasesRoutes(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			before := qBlockRetainedTokens.Load()
			cc := newQRouteContextConn(t)
			ctx, cancel := context.WithCancel(context.Background())
			wantErr := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 250*time.Millisecond)
				wantErr = context.DeadlineExceeded
			}
			defer cancel()
			exchange, err := cc.OpenQBlockExchange(ctx, []byte("context"))
			require.NoError(t, err)
			defer exchange.Close()
			_, err = exchange.NewToken()
			require.NoError(t, err)
			queued := cc.AcquireMessage(context.Background())
			queued.SetCode(codes.Content)
			queued.SetToken([]byte("context"))
			require.True(t, cc.routeQBlockResponse(queued))
			cc.ReleaseMessage(queued)
			if !deadline {
				cancel()
			}
			<-ctx.Done()
			_, err = exchange.NewToken()
			require.ErrorIs(t, err, context.Canceled)
			require.Eventually(t, func() bool {
				cc.qRoutesMu.Lock()
				defer cc.qRoutesMu.Unlock()
				return len(cc.qRoutes) == 0 && cc.qActiveRoutes == 0
			}, time.Second, time.Millisecond)
			_, err = exchange.Receive(context.Background())
			require.ErrorIs(t, err, wantErr)
			require.Equal(t, before, qBlockRetainedTokens.Load())
		})
	}
}

func TestQBlockExchangeResponseUsesReceiveContext(t *testing.T) {
	cc := newQRouteContextConn(t)
	exchange, err := cc.OpenQBlockExchange(context.Background(), []byte("context"))
	require.NoError(t, err)
	defer exchange.Close()
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		response := cc.AcquireMessage(context.Background())
		response.SetCode(codes.Content)
		response.SetToken([]byte("context"))
		require.True(t, cc.routeQBlockResponse(response))
		cc.ReleaseMessage(response)
		got, receiveErr := exchange.Receive(ctx)
		require.NoError(t, receiveErr)
		require.Same(t, ctx, got.Context())
		cc.ReleaseMessage(got)
		cancel()
	}
}
