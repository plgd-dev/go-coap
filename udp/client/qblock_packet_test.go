package client

import (
	"bytes"
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockPacketQ1SelectsSZXBeforeSending(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mtu    uint16
		path   string
		want   blockwise.SZX
		reject bool
	}{
		{name: "fits 32", mtu: 68, path: "/upload", want: blockwise.SZX32},
		{name: "options force 16", mtu: 68, path: "/long-upload-path-123456789", want: blockwise.SZX16},
		{name: "no block fits", mtu: 20, path: "/upload", reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cfg := DefaultConfig
			cfg.BlockwiseEnable, cfg.BlockwiseSZX, cfg.MTU = false, blockwise.SZX1024, tt.mtu
			cfg.GetToken = message.GetToken
			cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), ScheduleMode: qblockScheduleManual, GetRequestTag: func() (message.Token, error) { return message.Token{1}, nil }}))
			t.Cleanup(session.closeForTest)
			body := bytes.Repeat([]byte{'u'}, 96)
			req := newPOSTWithBody(t, cc, message.Token{1}, body)
			defer cc.ReleaseMessage(req)
			require.NoError(t, req.SetPath(tt.path))
			prepared, err := cc.qblockClient.prepare(req, nil)
			if tt.reject {
				require.Error(t, err)
				require.False(t, prepared.Prepared)
				require.Empty(t, session.writesSnapshot())
				require.Zero(t, cc.qblockClient.active())
				require.Empty(t, cc.qblockClient.workQueue.slots)
				return
			}
			require.NoError(t, err)
			writes := session.writesSnapshot()
			require.NotEmpty(t, writes)
			var assembled []byte
			for i, write := range writes {
				block, err := qblock.DecodeBlock(write.block)
				require.NoError(t, err)
				require.Equal(t, tt.want, block.SZX)
				require.Equal(t, uint32(i), block.Number)
				wire, err := coder.DefaultCoder.Size(message.Message{Token: write.token, Options: write.options, Payload: write.payload})
				require.NoError(t, err)
				require.LessOrEqual(t, wire, int(tt.mtu))
				assembled = append(assembled, write.payload...)
			}
			require.Equal(t, body, assembled)
		})
	}
}

func TestQBlockPacketServerQ2SelectsSZXBeforeSending(t *testing.T) {
	body := bytes.Repeat([]byte{'r'}, 96)
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(body)))
	})
	// 4 header + 8 token + 9 ETag + 1 Content-Format + 3 Size2
	// + 2 QBlock2 + 1 marker + 32 payload = 60 bytes.
	h.cc.qblockClient.datagramLimit = 60
	request := h.q1(t, 1, 0, false, 4, "body")
	request.SetOptionUint32(message.QBlock1, uint32(blockwise.SZX1024))
	h.ingest(request)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3)
	var assembled []byte
	for _, write := range writes {
		block, err := qblock.DecodeBlock(write.block)
		require.NoError(t, err)
		require.Equal(t, blockwise.SZX32, block.SZX)
		wire, err := coder.DefaultCoder.Size(message.Message{Token: write.token, Options: write.options, Payload: write.payload})
		require.NoError(t, err)
		require.LessOrEqual(t, wire, 60)
		assembled = append(assembled, write.payload...)
	}
	require.Equal(t, body, assembled)
}

func TestQBlockPacketOversizedWritesAreRejected(t *testing.T) {
	for _, paced := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "paced"}[paced], func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			t.Cleanup(session.closeForTest)
			cc.qblockClient.datagramLimit = 20
			var key qblockProbeKey
			if paced {
				key = 1
			}
			err := cc.qblockClient.writeServerQ2Block(context.Background(), message.Token{1}, 1, codes.Content, nil, blockwise.SZX16, 32, []byte("12345678"), qblock.Action{Block: qblock.Block{More: true}, Payload: bytes.Repeat([]byte{'x'}, 16)}, key)
			require.ErrorIs(t, err, qblock.ErrLimitExceeded)
			require.Empty(t, session.writesSnapshot())
			require.Zero(t, cc.qblockClient.probeGate.bytes)
		})
	}
}

func TestQBlockPacketSZXReservesLaterBlockOptionGrowth(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	template := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(template)
	// 4 header + 8 token + 3 option + 1 marker + 32 payload = 48.
	// Block 16 requires another option byte, so 544 bytes must use SZX16.
	cc.qblockClient.datagramLimit = 48
	szx, err := cc.qblockClient.selectBodySZX(template, message.QBlock1, 512, blockwise.SZX32)
	require.NoError(t, err)
	require.Equal(t, blockwise.SZX32, szx)
	szx, err = cc.qblockClient.selectBodySZX(template, message.QBlock1, 544, blockwise.SZX32)
	require.NoError(t, err)
	require.Equal(t, blockwise.SZX16, szx)
}

func TestQBlockPacketServerNoFitPreservesSuppression(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 32))))
	})
	h.cc.qblockClient.datagramLimit = 20
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	h.ingest(h.q1(t, 2, 0, false, 4, "body"))
	require.Equal(t, 1, calls)
	require.Empty(t, h.session.writesSnapshot())
	snapshot := h.snapshot()
	require.Zero(t, snapshot.active)
	require.Zero(t, snapshot.managerTokens)
	require.Zero(t, snapshot.managerBytes)
	require.Zero(t, snapshot.mids)
	require.Zero(t, snapshot.reservations)
	require.Equal(t, 1, snapshot.records)
	require.Empty(t, h.cc.qblockClient.workQueue.slots)
}
