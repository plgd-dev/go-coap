package qblock

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

type contextExchange struct {
	qObserveExchange
	receive func(context.Context) *pool.Message
}

func (e *contextExchange) Receive(ctx context.Context) (*pool.Message, error) {
	return e.receive(ctx), nil
}

func TestQBlockResponseContextOutlivesReceiveDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var receiveDone <-chan struct{}
	exchange := &contextExchange{receive: func(receiveCtx context.Context) *pool.Message {
		receiveDone = receiveCtx.Done()
		return pool.NewMessage(receiveCtx)
	}}
	response, err := receiveQBlockResponse(ctx, exchange, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Same(t, ctx, response.Context())
	require.NoError(t, response.Context().Err())
	select {
	case <-receiveDone:
	default:
		t.Fatal("receive context must be canceled after returning")
	}
	cancel()
	require.ErrorIs(t, response.Context().Err(), context.Canceled)
}

type contextTransport struct {
	*qTestTransport
	onSend func(context.Context)
}

func (c *contextTransport) SendQBlock(msg *pool.Message) error {
	c.onSend(msg.Context())
	return c.qTestTransport.SendQBlock(msg)
}

func TestQBlockScheduledSetsPreserveContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := 0
	cc := &contextTransport{
		qTestTransport: &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)},
		onSend: func(sendCtx context.Context) {
			require.Same(t, ctx, sendCtx)
			require.NoError(t, sendCtx.Err())
			sent++
		},
	}
	engine := New(cc, func(err error) { t.Error(err) })
	defer engine.CloseQBlockTransfers()
	config := DefaultConfig()
	config.MaxPayloads = 1
	require.NoError(t, engine.SetConfig(config))
	response := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(response)
	response.SetCode(codes.Content)
	response.SetBody(bytes.NewReader(make([]byte, 48)))
	require.NoError(t, engine.cacheQBlock2Response(ctx, "context", "identity", response, message.NonConfirmable))
	require.NoError(t, engine.scheduleQ2Set(ctx, &q2SetJob[*contextTransport]{
		transport: cc, key: "context", token: []byte("token"), szx: SZX16, mode: message.NonConfirmable,
		nums: []int64{0}, count: 3, wholeBody: true,
	}, time.Now()))
	for range 3 {
		job, ok := cc.jobs["q2:context"]
		require.True(t, ok)
		delete(cc.jobs, "q2:context")
		require.NoError(t, job.run())
	}
	require.Empty(t, cc.jobs)
	require.Equal(t, 3, sent)
}
