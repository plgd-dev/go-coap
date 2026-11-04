package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestQBlockAsyncSendCapacityAndClosedConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc := &Conn{session: qContextSession{ctx: ctx}, qAsyncSends: 128}
	msg := qControlMessage()
	msg.SetType(message.Confirmable)
	require.ErrorIs(t, cc.SendQBlockAsync(msg, func(error) {
		t.Error("rejected send must not invoke its completion")
	}), blockwise.ErrQBlockScheduleFull)
	require.Equal(t, 128, cc.qAsyncSends)
	cancel()
	cc.qAsyncSends = 0
	require.ErrorIs(t, cc.SendQBlockAsync(msg, func(error) {
		t.Error("closed connection must reject synchronously")
	}), context.Canceled)
	require.Zero(t, cc.qAsyncSends)
}
