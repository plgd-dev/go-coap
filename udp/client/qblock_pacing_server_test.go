package client

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestQBlockPacedServerSharesClientGate(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	handlerCalls := 0
	h := newServerHarness(t, managerConfig, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	})
	clientRequest := newPOSTWithBody(t, h.cc, message.Token{0x70}, bytes.Repeat([]byte{'u'}, 48))
	defer h.cc.ReleaseMessage(clientRequest)
	preparation, err := h.cc.qblockClient.prepare(clientRequest, nil)
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	clientWrites := h.session.writesSnapshot()
	require.Len(t, clientWrites, 2)

	h.ingest(h.q1(t, 0x71, 0, false, 4, "body"))
	require.Equal(t, 1, handlerCalls)
	require.Len(t, h.session.writesSnapshot(), 2, "server body must wait behind the client body gate")
	h.cc.qblockClient.mu.Lock()
	slots := len(h.cc.qblockClient.workQueue.slots)
	records := len(h.cc.qblockClient.server.records)
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, 2, slots)
	require.Equal(t, 1, records)

	feedback := q1Continue(t, h.cc, clientWrites[0].token, 1)
	require.True(t, h.cc.qblockClient.handle(feedback))
	h.cc.ReleaseMessage(feedback)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 5)
	require.Equal(t, message.Token{0x71}, writes[3].token)
	require.Equal(t, message.Token{0x71}, writes[4].token)
	require.NotEqual(t, writes[3].mid, writes[4].mid)
}

func TestQBlockPacedServerSettlesBeforeResponseAdmission(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	retention := 2 * managerConfig.Transfer.Lifetime
	handlerCalls := 0
	h := newServerHarness(t, managerConfig, qblockServerConfig{Retention: retention}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
	})
	clientRequest := newPOSTWithBody(t, h.cc, message.Token{0x72}, bytes.Repeat([]byte{'u'}, 48))
	defer h.cc.ReleaseMessage(clientRequest)
	_, err := h.cc.qblockClient.prepare(clientRequest, nil)
	require.NoError(t, err)
	h.ingest(h.q1(t, 0x73, 0, false, 4, "body"))
	require.Equal(t, 1, handlerCalls)
	require.Len(t, h.session.writesSnapshot(), 2)
	h.cc.qblockClient.mu.Lock()
	var record *qblockServerRecord
	for _, candidate := range h.cc.qblockClient.server.records {
		record = candidate
	}
	var running bool
	var retainUntil time.Time
	if record != nil {
		running = record.handlerRunning
		retainUntil = record.retainUntil
	}
	h.cc.qblockClient.mu.Unlock()
	require.NotNil(t, record)
	require.False(t, running)
	require.Equal(t, h.now.Add(retention), retainUntil)

	h.advance(managerConfig.Transfer.Lifetime)
	require.Equal(t, uint32(0), h.snapshot().active)
	require.Len(t, h.session.writesSnapshot(), 2)
	h.cc.qblockClient.mu.Lock()
	terminal := record.terminal
	deadline := record.expires
	slots := len(h.cc.qblockClient.workQueue.slots)
	h.cc.qblockClient.mu.Unlock()
	require.True(t, terminal)
	require.Equal(t, retainUntil, deadline)
	require.Zero(t, slots)
	h.ingest(h.q1(t, 0x74, 0, false, 4, "body"))
	require.Equal(t, 1, handlerCalls, "duplicate upload must not rerun the handler after an unsent response expires")
}

func TestQBlockPacedServerContinueBypassesDebt(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	h := newServerHarness(t, managerConfig, qblockServerConfig{}, nil)
	clientRequest := newPOSTWithBody(t, h.cc, message.Token{0x75}, bytes.Repeat([]byte{'u'}, 48))
	defer h.cc.ReleaseMessage(clientRequest)
	_, err := h.cc.qblockClient.prepare(clientRequest, nil)
	require.NoError(t, err)
	require.Len(t, h.session.writesSnapshot(), 2)
	h.ingest(h.q1(t, 0x76, 0, true, 48, "abcdefghijklmnop"))
	h.ingest(h.q1(t, 0x77, 1, true, 48, "qrstuvwxyzabcdef"))
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3)
	require.Equal(t, codes.Continue, writes[2].code)
	require.Equal(t, message.Token{0x77}, writes[2].token)
}

func TestQBlockPacedServerMissingControlWaitsForPermit(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	h := newServerHarness(t, managerConfig, qblockServerConfig{}, nil)
	h.cc.qblockClient.pacingConfig.NonProbingWait = 10 * time.Second
	clientRequest := newPOSTWithBody(t, h.cc, message.Token{0x78}, bytes.Repeat([]byte{'u'}, 48))
	defer h.cc.ReleaseMessage(clientRequest)
	_, err := h.cc.qblockClient.prepare(clientRequest, nil)
	require.NoError(t, err)
	require.Len(t, h.session.writesSnapshot(), 2)
	h.ingest(h.q1(t, 0x79, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(managerConfig.Transfer.NonReceiveTimeout)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3, "the client may advance its body, but NON 4.08 must wait")
	for _, write := range writes {
		require.NotEqual(t, codes.RequestEntityIncomplete, write.code)
	}
	h.cc.qblockClient.mu.Lock()
	var record *qblockServerRecord
	for _, candidate := range h.cc.qblockClient.server.records {
		record = candidate
	}
	var pending []qblock.ControlIntent
	if record != nil {
		pending = h.cc.qblockClient.manager.PendingControls(record.id)
	}
	h.cc.qblockClient.mu.Unlock()
	require.NotNil(t, record)
	require.Len(t, pending, 1, "retry intent must stay uncommitted while it waits")

	h.cc.qblockClient.mu.Lock()
	gateDeadline, ready := h.cc.qblockClient.probeGate.nextDeadline()
	h.cc.qblockClient.mu.Unlock()
	require.True(t, ready)
	h.advance(gateDeadline.Sub(h.now))
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 4)
	require.Equal(t, codes.RequestEntityIncomplete, writes[3].code)
	require.Equal(t, message.Token{0x79}, writes[3].token)
	h.cc.qblockClient.mu.Lock()
	pending = h.cc.qblockClient.manager.PendingControls(record.id)
	h.cc.qblockClient.mu.Unlock()
	require.Empty(t, pending)
}

func TestQBlockPacedServerRepairContextAndFeedback(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	h := newServerHarness(t, managerConfig, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	})
	h.ingest(h.q1(t, 0x81, 0, false, 4, "body"))
	require.Len(t, h.session.writesSnapshot(), 2)
	h.cc.qblockClient.mu.Lock()
	initialKey := h.cc.qblockClient.probeGate.key
	initialState := h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeActive, initialState)

	wrongIdentity := h.control(t, 0x82, 2, true, "other-tag")
	h.ingest(wrongIdentity)
	wrongSZX := h.control(t, 0x83, 2, true, "tag-a")
	value, err := qblock.EncodeBlock(qblock.Block{Number: 2, More: true, SZX: blockwise.SZX32})
	require.NoError(t, err)
	wrongSZX.SetOptionUint32(message.QBlock2, value)
	h.ingest(wrongSZX)
	require.Len(t, h.session.writesSnapshot(), 2)
	h.cc.qblockClient.mu.Lock()
	key := h.cc.qblockClient.probeGate.key
	state := h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, initialKey, key)
	require.Equal(t, qblockProbeActive, state)

	continueRequest := h.control(t, 0x84, 2, true, "tag-a")
	h.ingest(continueRequest)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3)
	require.Equal(t, message.Token{0x84}, writes[2].token)
	h.cc.qblockClient.mu.Lock()
	state = h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeOpen, state)

	h.ingest(h.control(t, 0x85, 0, false, "tag-a"))
	h.ingest(h.control(t, 0x86, 1, false, "tag-a"))
	h.advance(2 * time.Second)
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 5)
	require.Equal(t, message.Token{0x85}, writes[3].token)
	require.Equal(t, message.Token{0x86}, writes[4].token)
	require.NotEqual(t, writes[3].mid, writes[4].mid)
}

func TestQBlockPacedServerMissingFeedbackRequiresNewBlock(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 0x91, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(qblock.DefaultTransferConfig().NonReceiveTimeout)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.RequestEntityIncomplete, writes[0].code)
	h.cc.qblockClient.mu.Lock()
	currentKey := h.cc.qblockClient.probeGate.key
	state := h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeWaiting, state)

	h.ingest(h.q1(t, 0x91, 1, false, 32, "qrstuvwxyzabcdef"))
	h.cc.qblockClient.mu.Lock()
	key := h.cc.qblockClient.probeGate.key
	state = h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, currentKey, key)
	require.Equal(t, qblockProbeWaiting, state)

	h.ingest(h.q1(t, 0x92, 0, true, 32, "abcdefghijklmnop"))
	h.cc.qblockClient.mu.Lock()
	key = h.cc.qblockClient.probeGate.key
	state = h.cc.qblockClient.probeGate.state
	h.cc.qblockClient.mu.Unlock()
	require.Zero(t, key)
	require.Equal(t, qblockProbeOpen, state)
}

func TestQBlockPacedServerAdmissionRollsBackAtSharedLimit(t *testing.T) {
	config := qblock.DefaultManagerConfig()
	config.MaxTransfers = 1
	h := newServerHarness(t, config, qblockServerConfig{}, nil)
	request := newPrivateQBlockClientGET(t, h.cc, message.Token{0xf0})
	defer h.cc.ReleaseMessage(request)
	preparation, err := h.cc.qblockClient.prepare(request, nil)
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	before := h.snapshot()
	h.cc.qblockClient.mu.Lock()
	bytesBefore := h.cc.qblockClient.workQueue.used
	slotsBefore := len(h.cc.qblockClient.workQueue.slots)
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, 1, slotsBefore)
	h.ingest(h.q1(t, 0xf1, 0, false, 4, "body"))
	require.Equal(t, before, h.snapshot(), "rejected first fragment must leave no server state")
	h.cc.qblockClient.mu.Lock()
	bytesAfter := h.cc.qblockClient.workQueue.used
	slotsAfter := len(h.cc.qblockClient.workQueue.slots)
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, bytesBefore, bytesAfter)
	require.Equal(t, slotsBefore, slotsAfter)
	for _, write := range h.session.writesSnapshot() {
		require.NotEqual(t, message.Token{0xf1}, write.token, "rejected server fragment must not add a write")
	}
}
