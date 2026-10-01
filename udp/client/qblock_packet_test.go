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
		w.Message().SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
	})
	// The incoming upload fits; only the captured response cannot fit.
	h.cc.qblockClient.datagramLimit = 68
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

// Catches advertising blocks that cannot fit response metadata, and queuing
// an initial GET that can never pass the writer's complete-datagram guard.
func TestQBlockPacketGETSelectsSZXBeforeAdmission(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mtu    uint16
		path   string
		want   blockwise.SZX
		reject bool
	}{
		{name: "32 byte response", mtu: 68, path: "/q", want: blockwise.SZX32},
		{name: "16 byte response", mtu: 50, path: "/q", want: blockwise.SZX16},
		{name: "response cannot fit", mtu: 49, path: "/q", reject: true},
		{name: "request cannot fit", mtu: 68, path: "/" + string(bytes.Repeat([]byte{'p'}, 80)), reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cfg := DefaultConfig
			cfg.BlockwiseEnable, cfg.BlockwiseSZX, cfg.MTU = false, blockwise.SZX1024, tt.mtu
			cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), ScheduleMode: qblockScheduleManual}))
			t.Cleanup(session.closeForTest)
			req := newPrivateQBlockClientGET(t, cc, message.Token{1})
			defer cc.ReleaseMessage(req)
			require.NoError(t, req.SetPath(tt.path))
			prepared, err := cc.qblockClient.prepare(req, nil)
			if tt.reject {
				require.ErrorIs(t, err, qblock.ErrLimitExceeded)
				require.False(t, prepared.Prepared)
				require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
				require.Empty(t, cc.qblockClient.workQueue.slots)
				require.Empty(t, session.writesSnapshot())
				return
			}
			require.NoError(t, err)
			require.True(t, prepared.OwnsTransmission)
			cc.qblockClient.Tick(cc.qblockClient.now())
			writes := session.writesSnapshot()
			require.Len(t, writes, 1)
			block, err := qblock.DecodeBlock(writes[0].block)
			require.NoError(t, err)
			require.Equal(t, qblock.Block{Number: 0, More: true, SZX: tt.want}, block)
			cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
			requireQBlockClientFullyIdle(t, cc.qblockClient)
		})
	}
}

// Catches admitting a peer's larger SZX or unexpected metadata despite the
// advertised ceiling, and incorrectly treating either packet as feedback.
func TestQBlockPacketGETRejectsUnacceptableFirstResponse(t *testing.T) {
	for _, oversizedOptions := range []bool{false, true} {
		t.Run(map[bool]string{false: "larger SZX", true: "oversized options"}[oversizedOptions], func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cc := newPrivateQBlockClientConnWithTokenAndSZX(t, session, message.GetToken, blockwise.SZX1024)
			t.Cleanup(session.closeForTest)
			cc.qblockClient.datagramLimit = 68
			req := newPrivateQBlockClientGET(t, cc, message.Token{1})
			defer cc.ReleaseMessage(req)
			prepared, err := cc.qblockClient.prepare(req, nil)
			require.NoError(t, err)
			require.True(t, prepared.Prepared)
			cc.qblockClient.Tick(cc.qblockClient.now())
			exchange := cc.qblockClient.exchangesByOriginalToken[string(req.Token())]
			debt, owner, state := cc.qblockClient.probeGate.bytes, cc.qblockClient.probeGate.key, cc.qblockClient.probeGate.state
			szx := blockwise.SZX64
			if oversizedOptions {
				szx = blockwise.SZX16
			}
			bad := newQBlockClientFragmentWithSZX(t, cc, req.Token(), 0, true, 128, szx)
			defer cc.ReleaseMessage(bad)
			if oversizedOptions {
				bad.SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
			} else {
				// Small final payload fits the MTU despite a too-large SZX.
				bad.SetOptionUint32(message.QBlock2, uint32(blockwise.SZX64))
				bad.SetOptionUint32(message.Size2, 4)
				bad.SetBody(bytes.NewReader([]byte("body")))
			}
			require.True(t, cc.qblockClient.handle(bad))
			require.Zero(t, cc.qblockClient.active())
			require.Zero(t, qblockClientManagerRetainedBytesForTest(cc.qblockClient.manager))
			require.Same(t, exchange, cc.qblockClient.exchangesByOriginalToken[string(req.Token())])
			require.Equal(t, debt, cc.qblockClient.probeGate.bytes)
			require.Equal(t, owner, cc.qblockClient.probeGate.key)
			require.Equal(t, state, cc.qblockClient.probeGate.state)
			// A later first fragment choosing a smaller SZX is still accepted.
			valid := newQBlockClientFragmentWithSZX(t, cc, req.Token(), 0, true, 128, blockwise.SZX16)
			defer cc.ReleaseMessage(valid)
			require.True(t, cc.qblockClient.handle(valid))
			require.Equal(t, uint32(1), cc.qblockClient.active())
			for _, transfer := range cc.qblockClient.transfers {
				require.Equal(t, blockwise.SZX16, transfer.metadata.SZX)
			}
			cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
			requireQBlockClientFullyIdle(t, cc.qblockClient)
		})
	}
}

func TestQBlockPacketGETLargeBodyCapAllowsSmallResource(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable, cfg.BlockwiseSZX = false, blockwise.SZX16
	manager := qblock.DefaultManagerConfig()
	manager.Transfer.MaxBodySize = 32 << 20
	manager.MaxRetainedBytes = 64 << 20
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: manager, ScheduleMode: qblockScheduleManual}))
	t.Cleanup(session.closeForTest)
	req := newPrivateQBlockClientGET(t, cc, message.Token{1})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	response := newQBlockClientFragment(t, cc, req.Token(), 0, false, 16)
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	requireQBlockClientFullyIdle(t, cc.qblockClient)
}

func TestQBlockPacketGETRawDatagramLimitSurvivesOptionDiscard(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	t.Cleanup(session.closeForTest)
	cc.qblockClient.datagramLimit = 68
	req := newPrivateQBlockClientGET(t, cc, message.Token{1})
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	cc.qblockClient.Tick(cc.qblockClient.now())
	debt := cc.qblockClient.probeGate.bytes
	// Route synchronously at the monitor boundary to make this Process test
	// deterministic; the real decoder and Q2 adapter still execute.
	monitored := 0
	cc.requestMonitor = func(_ *Conn, msg *pool.Message) (bool, error) {
		monitored++
		cc.qblockClient.handle(msg)
		return true, nil
	}
	response := newQBlockClientFragment(t, cc, req.Token(), 0, true, 32)
	defer cc.ReleaseMessage(response)
	response.SetType(message.NonConfirmable)
	response.SetMessageID(101)
	response.SetOptionBytes(message.MaxAge, bytes.Repeat([]byte{'x'}, 80))
	raw, err := response.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.Greater(t, len(raw), 68)
	require.NoError(t, cc.Process(nil, raw))
	require.Zero(t, monitored)
	require.Zero(t, cc.qblockClient.active())
	require.Equal(t, debt, cc.qblockClient.probeGate.bytes)
	response.Remove(message.MaxAge)
	raw, err = response.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.NoError(t, cc.Process(nil, raw))
	require.Equal(t, 1, monitored)
	require.Equal(t, uint32(1), cc.qblockClient.active())
	cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
	requireQBlockClientFullyIdle(t, cc.qblockClient)
}

// Rejecting a too-large fragment must act like packet loss, not accepted
// progress or teardown. Both a live Q2 receiver and Q1 handoff need this.
func TestQBlockPacketIncomingClientLimits(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "follow-on GET", true: "upload handoff"}[upload], func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			t.Cleanup(session.closeForTest)
			cc.qblockClient.datagramLimit = 68
			var req *pool.Message
			if upload {
				req = newPOSTWithBody(t, cc, message.Token{1}, []byte("body"))
			} else {
				req = newPrivateQBlockClientGET(t, cc, message.Token{1})
			}
			defer cc.ReleaseMessage(req)
			_, err := cc.qblockClient.prepare(req, nil)
			require.NoError(t, err)
			cc.qblockClient.Tick(cc.qblockClient.now())
			token := req.Token()
			if upload {
				token = session.writesSnapshot()[0].token
			} else {
				first := newQBlockClientFragment(t, cc, token, 0, true, 32)
				require.True(t, cc.qblockClient.handle(first))
				cc.ReleaseMessage(first)
			}
			var id qblock.TransferID
			for liveID := range cc.qblockClient.transfers {
				id = liveID
			}
			before := cc.qblockClient.transfers[id]
			progress, _ := cc.qblockClient.manager.ReceiverProgress(id)
			debt := *cc.qblockClient.probeGate
			var bad *pool.Message
			if upload {
				bad = q2ResponseForPost(t, cc, token, 0, true, 32, 'r')
			} else {
				bad = newQBlockClientFragment(t, cc, token, 1, false, 32)
			}
			defer cc.ReleaseMessage(bad)
			bad.SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
			require.True(t, cc.qblockClient.handle(bad))
			require.Same(t, before, cc.qblockClient.transfers[id])
			after, _ := cc.qblockClient.manager.ReceiverProgress(id)
			require.Equal(t, progress, after)
			require.Equal(t, debt, *cc.qblockClient.probeGate)
			bad.Remove(message.LocationQuery)
			require.True(t, cc.qblockClient.handle(bad))
			if upload {
				require.Equal(t, uint32(1), cc.qblockClient.active())
				for _, tr := range cc.qblockClient.transfers {
					require.Equal(t, qblock.Q2, tr.kind)
				}
				cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
			}
			requireQBlockClientFullyIdle(t, cc.qblockClient)
		})
	}
}

func TestQBlockPacketIncomingServerLimits(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 32))))
	})
	h.cc.qblockClient.datagramLimit = 68
	first := h.q1(t, 1, 0, true, 32, "0123456789abcdef")
	first.SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
	require.True(t, h.cc.qblockClient.handleServerRequest(first))
	require.Equal(t, serverSnapshot{}, h.snapshot())
	first.Remove(message.LocationQuery)
	h.ingest(first)
	baseline := h.snapshot()
	last := h.q1(t, 2, 1, false, 32, "0123456789abcdef")
	last.SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
	require.True(t, h.cc.qblockClient.handleServerRequest(last))
	require.Equal(t, baseline, h.snapshot())
	require.Zero(t, calls)
	last.Remove(message.LocationQuery)
	h.ingest(last)
	require.Equal(t, 1, calls)
	baseline = h.snapshot()
	writes := len(h.session.writesSnapshot())
	control := h.control(t, 3, 0, false, "tag-a")
	control.SetOptionBytes(message.LocationQuery, bytes.Repeat([]byte{'x'}, 80))
	require.True(t, h.cc.qblockClient.handleServerRequest(control))
	require.Equal(t, baseline, h.snapshot())
	require.Len(t, h.session.writesSnapshot(), writes)
	control.Remove(message.LocationQuery)
	h.ingest(control)
	require.Greater(t, len(h.session.writesSnapshot()), writes)
	require.Equal(t, 1, calls)
}
