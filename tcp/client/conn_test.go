package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestConnReceiveCleanupPreservesOwnerAcquisition(t *testing.T) {
	ctx := context.Background()
	session := &Session{messagePool: pool.New(1, 2048)}
	session.ctx.Store(&ctx)
	cc := &Conn{session: session}
	for i := 0; i < 64; i++ {
		req := cc.AcquireMessage(ctx)
		req.SetCode(codes.Content)
		var owned *pool.Message
		cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			w.Message().SetModified(false)
			r.Hijack()
			cc.ReleaseMessage(r)
			owned = cc.AcquireMessage(ctx)
			owned.SetCode(codes.GET)
			owned.SetMessageID(42)
		})
		require.Equal(t, codes.GET, owned.Code(), "receive cleanup must not reset the owner's acquisition")
		require.Equal(t, int32(42), owned.MessageID())
		cc.ReleaseMessage(owned)
	}
}
