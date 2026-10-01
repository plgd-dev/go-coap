package client

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
	"net"
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

func TestQBlockServerHandlerPreservesRequestEnvelope(t *testing.T) {
	cm := &coapNet.ControlMessage{Dst: net.ParseIP("127.0.0.2"), Src: net.ParseIP("127.0.0.1"), IfIndex: 3}
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		require.Equal(t, message.Token{1}, r.Token())
		require.Equal(t, message.NonConfirmable, r.Type())
		require.EqualValues(t, 7, r.MessageID())
		require.EqualValues(t, 9, r.Sequence())
		require.Equal(t, cm, r.ControlMessage())
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
	})
	req := h.cc.AcquireMessage(h.cc.Context())
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(7)
	req.SetSequence(9)
	req.SetToken([]byte{1})
	req.SetControlMessage(cm)
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock2, 8)
	h.ingest(req)
}
func TestQBlockWritesPreserveConnectionControlInformation(t *testing.T) {
	session := &qblockControlCaptureSession{qblockTestSession: &qblockTestSession{ctx: context.Background()}}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig()}))
	t.Cleanup(session.closeForTest)
	cc.setControlInformation(&coapNet.ControlMessage{Dst: net.ParseIP("127.0.0.2"), IfIndex: 3})
	req := cc.AcquireMessage(cc.Context())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.Content)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	require.NoError(t, cc.qblockClient.writeQBlockMessage(req))
	require.NotNil(t, session.control)
	require.Equal(t, net.ParseIP("127.0.0.2"), session.control.Src)
	require.Equal(t, 3, session.control.IfIndex)
}

type qblockControlCaptureSession struct {
	*qblockTestSession
	control *coapNet.ControlMessage
}

func (s *qblockControlCaptureSession) WriteMessage(msg *pool.Message) error {
	s.control = msg.ControlMessage()
	return s.qblockTestSession.WriteMessage(msg)
}

func TestQBlockServerInitialGETExpiryRemovesActiveDeadline(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	req := h.cc.AcquireMessage(h.cc.Context())
	defer h.cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	req.SetToken([]byte{1})
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock2, 8)
	_, ok := h.cc.qblockClient.server.handleInitialGET(req)
	require.True(t, ok)
	h.cc.qblockClient.mu.Lock()
	var record *qblockServerRecord
	for _, r := range h.cc.qblockClient.server.records {
		record = r
	}
	record.executing = true
	record.handlerRunning = true
	h.cc.qblockClient.mu.Unlock()
	h.advance(h.cc.qblockClient.managerConfig.Transfer.Lifetime)
	h.cc.qblockClient.mu.Lock()
	terminal := record.terminal
	_, active := h.cc.qblockClient.server.nextRecordDeadlineLocked()
	h.cc.qblockClient.mu.Unlock()
	require.True(t, terminal)
	require.False(t, active, "blocked handler cannot leave overdue active GET deadline")
}
