package client

import (
	"bytes"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQBlockServerInitialGETAndRepair(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		calls++
		require.Equal(t, codes.GET, r.Code())
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	})
	get := func(token byte, number uint32, more bool) *pool.Message {
		req := h.cc.AcquireMessage(h.cc.Context())
		req.SetCode(codes.GET)
		req.SetType(message.NonConfirmable)
		req.SetToken(message.Token{token})
		req.SetMessageID(int32(token))
		require.NoError(t, req.SetPath("/get"))
		req.SetOptionBytes(message.RequestTag, []byte{1})
		value, err := qblock.EncodeBlock(qblock.Block{Number: number, More: more, SZX: 0})
		require.NoError(t, err)
		req.SetOptionUint32(message.QBlock2, value)
		return req
	}
	h.ingest(get(1, 0, true))
	require.Equal(t, 1, calls)
	require.Len(t, h.session.writesSnapshot(), 3)
	h.ingest(get(2, 1, false))
	require.Equal(t, 1, calls)
	require.Len(t, h.session.writesSnapshot(), 4)
	require.Equal(t, message.Token{2}, h.session.writesSnapshot()[3].token)
	h.ingest(get(3, 0, true))
	require.Equal(t, 1, calls, "initial duplicate does not reinvoke")
}

func TestQBlockServerInitialGETHandlerExpiresBeforeResponse(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {})
	h.cc.qblockClient.server.handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		h.advance(h.cc.qblockClient.managerConfig.Transfer.Lifetime)
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("late"))))
	}
	req := h.cc.AcquireMessage(h.cc.Context())
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	req.SetToken([]byte{1})
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock2, 8)
	h.ingest(req)
	require.Empty(t, h.session.writesSnapshot(), "expired GET handler cannot start new sender")
}
