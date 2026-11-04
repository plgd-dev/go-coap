package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestReceivedMessageMetadataCapturedBeforeDelivery(t *testing.T) {
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.Errors = func(err error) { t.Error(err) }
	cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
		// Simulate immediate reuse after transferring ownership to a handler.
		req.Reset()
		req.SetOptionUint32(message.QBlock2, 0)
	}
	cc := NewConnWithOpts(session, &cfg)
	cc.qDatagramLimit = 1
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(123)
	req.SetToken([]byte("owner"))
	cc.ProcessReceivedMessageWithHandler(req, cc.handleReq)

	written := session.lastWritten()
	require.NotNil(t, written)
	require.Equal(t, codes.Content, written.Code(),
		"reused input options must not enable Q datagram validation")
	require.Equal(t, message.Confirmable, written.Type(),
		"reused input options must not change the original response mode")
}
