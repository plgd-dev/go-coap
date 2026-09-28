package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

func TestQBlockPacedQ1PreparationAndHandoff(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	jitterCalls := 0
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager:      qblock.DefaultManagerConfig(),
		Clock:        clock,
		ScheduleMode: qblockScheduleManual,
		Pacing:       &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
		Jitter: func() float64 {
			jitterCalls++
			return 0.5
		},
	})
	cc.qblockClient.mu.Lock()
	admitted := cc.qblockClient.probeGate.admit(77, qblockProbeControl, 0, now)
	cc.qblockClient.probeGate.charge(77, 20)
	cc.qblockClient.probeGate.settle(77, now)
	cc.qblockClient.mu.Unlock()
	require.True(t, admitted)

	req := newPOSTWithBody(t, cc, message.Token{0xb1}, bytes.Repeat([]byte{'u'}, 16))
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	require.Equal(t, 1, jitterCalls)
	require.Empty(t, session.writesSnapshot())
	require.Equal(t, uint32(1), cc.qblockClient.active())
	cc.qblockClient.mu.Lock()
	exchange := cc.qblockClient.exchangesByOriginalToken[string(req.Token())]
	queueLen := len(cc.qblockClient.workQueue.slots)
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, exchange)
	workID := exchange.workID
	require.NotZero(t, workID)
	require.Equal(t, 1, queueLen)

	clock.Advance(19 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	require.Empty(t, session.writesSnapshot())
	clock.Advance(time.Second)
	cc.qblockClient.Tick(clock.Now())
	writes := session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, []byte{'u'}, writes[0].payload[:1])
	require.Equal(t, 1, jitterCalls)

	response := q2ResponseForPost(t, cc, writes[0].token, 0, true, 32, 'r')
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	cc.qblockClient.mu.Lock()
	handoffID := exchange.workID
	queueLen = len(cc.qblockClient.workQueue.slots)
	transferCount := len(exchange.transfers)
	var kind qblock.Kind
	for id := range exchange.transfers {
		kind = cc.qblockClient.transfers[id].kind
	}
	cc.qblockClient.mu.Unlock()
	require.Equal(t, workID, handoffID)
	require.Equal(t, 1, queueLen)
	require.Equal(t, 1, transferCount)
	require.Equal(t, qblock.Q2, kind)
}

func TestQBlockPacedCanceledPartialBodySettlesDebt(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	request := newPOSTWithBody(t, cc, message.Token{0xb2}, bytes.Repeat([]byte{'x'}, 48))
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, nil)
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	require.Len(t, session.writesSnapshot(), 2)
	cc.qblockClient.mu.Lock()
	initialState := cc.qblockClient.probeGate.state
	initialBytes := cc.qblockClient.probeGate.bytes
	cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeActive, initialState)
	require.Positive(t, initialBytes)

	cc.qblockClient.abandon(request.Token(), qblock.ErrCanceled)
	cc.qblockClient.mu.Lock()
	state := cc.qblockClient.probeGate.state
	debt := cc.qblockClient.probeGate.bytes
	deadline, hasDeadline := cc.qblockClient.probeGate.nextDeadline()
	queueLen := len(cc.qblockClient.workQueue.slots)
	cc.qblockClient.mu.Unlock()
	require.Zero(t, cc.qblockClient.active())
	require.Zero(t, queueLen)
	require.Equal(t, qblockProbeWaiting, state)
	require.Equal(t, initialBytes, debt)
	require.True(t, hasDeadline)
	require.True(t, deadline.After(now))
}

func TestQBlockPacedFeedbackRequiresProgress(t *testing.T) {
	cc, session := startThreeBlockQ1(t)
	defer cc.qblockClient.close()
	writes := session.writesSnapshot()
	require.Len(t, writes, 3)
	token := writes[0].token
	cc.qblockClient.mu.Lock()
	initialKey := cc.qblockClient.probeGate.key
	initialState := cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeActive, initialState)

	malformed := q2ResponseForPost(t, cc, token, 0, true, 32, 'a')
	malformed.Remove(message.ETag)
	require.True(t, cc.qblockClient.handle(malformed))
	cc.ReleaseMessage(malformed)
	stale := q1Continue(t, cc, token, 1)
	require.True(t, cc.qblockClient.handle(stale))
	cc.ReleaseMessage(stale)
	cc.qblockClient.mu.Lock()
	gotKey := cc.qblockClient.probeGate.key
	gotState := cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, initialKey, gotKey)
	require.Equal(t, qblockProbeActive, gotState)
	require.Len(t, session.writesSnapshot(), 3)

	accepted := q1Continue(t, cc, token, 2)
	require.True(t, cc.qblockClient.handle(accepted))
	cc.ReleaseMessage(accepted)
	require.Len(t, session.writesSnapshot(), 6)
	cc.qblockClient.mu.Lock()
	gotKey = cc.qblockClient.probeGate.key
	gotState = cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Zero(t, gotKey)
	require.Equal(t, qblockProbeOpen, gotState)
}

func TestQBlockPacedQ1FeedbackDrivesQueuedGET(t *testing.T) {
	cc, session := startThreeBlockQ1(t)
	defer cc.qblockClient.close()
	writes := session.writesSnapshot()
	require.Len(t, writes, 3)
	queuedToken := message.Token{0xf3}
	request := newPrivateQBlockClientGET(t, cc, queuedToken)
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, nil)
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(cc.qblockClient.now())
	require.Len(t, session.writesSnapshot(), 3)

	feedback := q1Continue(t, cc, writes[0].token, 2)
	require.True(t, cc.qblockClient.handle(feedback))
	cc.ReleaseMessage(feedback)
	writes = session.writesSnapshot()
	require.Len(t, writes, 7, "valid Q1 feedback should immediately drive pending client work")
	require.Equal(t, queuedToken, writes[6].token)
}

func TestQBlockPacedFeedbackRequiresCurrentSet(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 3
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
		Jitter: func() float64 { return 0 },
	})
	request := newPOSTWithBody(t, cc, message.Token{0xc1}, bytes.Repeat([]byte{'x'}, 144))
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	writes := session.writesSnapshot()
	require.Len(t, writes, 3)
	firstToken := writes[0].token
	cc.qblockClient.mu.Lock()
	due, ok := cc.qblockClient.manager.NextDeadline()
	cc.qblockClient.mu.Unlock()
	require.True(t, ok)
	clock.Advance(due.Sub(clock.Now()))
	cc.qblockClient.Tick(clock.Now())
	require.Len(t, session.writesSnapshot(), 6)
	cc.qblockClient.mu.Lock()
	probe := cc.qblockClient.currentProbe
	currentKey := cc.qblockClient.probeGate.key
	currentState := cc.qblockClient.probeGate.state
	bytesBeforeRepair := cc.qblockClient.probeGate.bytes
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, probe)
	require.Equal(t, uint32(1), probe.set)
	require.Equal(t, qblockProbeActive, currentState)

	staleReport := q1Missing(t, cc, firstToken, []uint32{0})
	require.True(t, cc.qblockClient.handle(staleReport))
	cc.ReleaseMessage(staleReport)
	cc.qblockClient.mu.Lock()
	state := cc.qblockClient.probeGate.state
	key := cc.qblockClient.probeGate.key
	bytesAfterRepair := cc.qblockClient.probeGate.bytes
	cc.qblockClient.mu.Unlock()
	require.Equal(t, currentState, state)
	require.Equal(t, currentKey, key)
	require.Greater(t, bytesAfterRepair, bytesBeforeRepair)
	require.Equal(t, uint32(1), cc.qblockClient.active(), "a valid older-set repair must not cancel the sender")
	require.Len(t, session.writesSnapshot(), 7, "the requested repair must still be transmitted")
	cc.qblockClient.mu.Lock()
	due, ok = cc.qblockClient.manager.NextDeadline()
	cc.qblockClient.mu.Unlock()
	require.True(t, ok)
	clock.Advance(due.Sub(clock.Now()))
	cc.qblockClient.Tick(clock.Now())
	cc.qblockClient.mu.Lock()
	bytesAfterFinalSet := cc.qblockClient.probeGate.bytes
	cc.qblockClient.mu.Unlock()
	require.Greater(t, bytesAfterFinalSet, bytesAfterRepair)
}

func TestQBlockPacedQ2ControlBatch(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager:      managerConfig,
		Clock:        clock,
		ScheduleMode: qblockScheduleManual,
		Pacing:       &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xd1}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	cc.qblockClient.drivePending(now)
	require.Len(t, session.writesSnapshot(), 1)

	first := newQBlockClientFragment(t, cc, token, 2, true, 64)
	defer cc.ReleaseMessage(first)
	require.True(t, cc.qblockClient.handle(first))
	writes := session.writesSnapshot()
	require.Len(t, writes, 2, "only the first missing-block request may use the open gate")
	require.NotEqual(t, token, writes[1].token)
	block, err := qblock.DecodeBlock(writes[1].block)
	require.NoError(t, err)
	require.Equal(t, uint32(0), block.Number)
	cc.qblockClient.mu.Lock()
	transfer := cc.qblockClient.transferByToken[string(token)]
	var intents []qblock.ControlIntent
	if transfer != nil {
		intents = cc.qblockClient.manager.PendingControls(transfer.id)
	}
	deadline, ok := cc.qblockClient.probeGate.nextDeadline()
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, transfer)
	require.Len(t, intents, 1, "the batch cannot commit after only one packet")
	require.True(t, ok)

	clock.Advance(deadline.Sub(clock.Now()))
	cc.qblockClient.Tick(clock.Now())
	writes = session.writesSnapshot()
	require.Len(t, writes, 3)
	require.NotEqual(t, token, writes[2].token)
	require.NotEqual(t, writes[1].token, writes[2].token)
	block, err = qblock.DecodeBlock(writes[2].block)
	require.NoError(t, err)
	require.Equal(t, uint32(1), block.Number)
	cc.qblockClient.mu.Lock()
	intents = cc.qblockClient.manager.PendingControls(transfer.id)
	cc.qblockClient.mu.Unlock()
	require.Empty(t, intents)
}

func TestQBlockPacedFullRecoveryBatchFitsReservedCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 10
	config.Transfer.Lifetime = time.Hour
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xd4}
	request := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, nil)
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	cc.qblockClient.mu.Lock()
	cc.qblockClient.workQueue.maxBytes = cc.qblockClient.workQueue.used
	cc.qblockClient.mu.Unlock()
	fragment := newQBlockClientFragment(t, cc, token, 10, true, 192)
	require.True(t, cc.qblockClient.handle(fragment))
	cc.ReleaseMessage(fragment)
	require.Equal(t, uint32(1), cc.qblockClient.active(), "reserved bytes must cover the full recovery intent")
	require.Len(t, session.writesSnapshot(), 2, "first missing-block request must be sent")
	for packet := 1; packet < 10; packet++ {
		cc.qblockClient.mu.Lock()
		deadline, ok := cc.qblockClient.probeGate.nextDeadline()
		cc.qblockClient.mu.Unlock()
		require.True(t, ok)
		clock.Advance(deadline.Sub(clock.Now()))
		cc.qblockClient.Tick(clock.Now())
		writes := session.writesSnapshot()
		require.Len(t, writes, packet+2)
		block, err := qblock.DecodeBlock(writes[packet+1].block)
		require.NoError(t, err)
		require.Equal(t, uint32(packet), block.Number)
		require.Equal(t, uint32(1), cc.qblockClient.active())
	}
	cc.qblockClient.mu.Lock()
	transfer := cc.qblockClient.transferByToken[string(token)]
	var intents []qblock.ControlIntent
	if transfer != nil {
		intents = cc.qblockClient.manager.PendingControls(transfer.id)
	}
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, transfer)
	require.Empty(t, intents, "the retry commits only after packet ten")
}

func TestQBlockPacedQ2ContinuationUsesGate(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xd2}
	request := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	require.Len(t, session.writesSnapshot(), 1)
	first := newQBlockClientFragment(t, cc, token, 0, true, 64)
	require.True(t, cc.qblockClient.handle(first))
	cc.ReleaseMessage(first)
	require.Len(t, session.writesSnapshot(), 1)
	cc.qblockClient.mu.Lock()
	admitted := cc.qblockClient.probeGate.admit(99, qblockProbeControl, 0, now)
	cc.qblockClient.probeGate.charge(99, 20)
	cc.qblockClient.probeGate.settle(99, now)
	cc.qblockClient.mu.Unlock()
	require.True(t, admitted)

	second := newQBlockClientFragment(t, cc, token, 1, true, 64)
	require.True(t, cc.qblockClient.handle(second))
	cc.ReleaseMessage(second)
	require.Len(t, session.writesSnapshot(), 1, "Q2 continuation must wait behind existing control debt")
	clock.Advance(19 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	require.Len(t, session.writesSnapshot(), 1)
	clock.Advance(time.Second)
	cc.qblockClient.Tick(clock.Now())
	writes := session.writesSnapshot()
	require.Len(t, writes, 2)
	require.NotEqual(t, token, writes[1].token)
	block, err := qblock.DecodeBlock(writes[1].block)
	require.NoError(t, err)
	require.Equal(t, uint32(2), block.Number)
}

func TestQBlockPacedClientRejectsImmediateControlOutput(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xd3}
	request := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	first := newQBlockClientFragment(t, cc, token, 0, true, 64)
	require.True(t, cc.qblockClient.handle(first))
	cc.ReleaseMessage(first)
	cc.qblockClient.mu.Lock()
	transfer := cc.qblockClient.transferByToken[string(token)]
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, transfer)
	cc.qblockClient.drive([]qblock.Output{{
		TransferID: transfer.id, Operation: transfer.operation,
		Action: qblock.Action{Kind: qblock.SendContinue, Through: 0},
	}})
	require.Len(t, session.writesSnapshot(), 1, "unexpected immediate control must not bypass pacing")
	require.Zero(t, cc.qblockClient.active())
}

func TestQBlockPacedFeedbackRequiresReceiverProgress(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: managerConfig, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	requestToken := message.Token{0xe1}
	request := newPrivateQBlockClientGET(t, cc, requestToken)
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	cc.qblockClient.drivePending(now)
	first := newQBlockClientFragment(t, cc, requestToken, 2, true, 64)
	require.True(t, cc.qblockClient.handle(first))
	cc.ReleaseMessage(first)
	writes := session.writesSnapshot()
	require.Len(t, writes, 2)
	controlToken := writes[1].token
	cc.qblockClient.mu.Lock()
	currentKey := cc.qblockClient.probeGate.key
	state := cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeWaiting, state)

	duplicate := newQBlockClientFragment(t, cc, requestToken, 2, true, 64)
	require.True(t, cc.qblockClient.handle(duplicate))
	cc.ReleaseMessage(duplicate)
	cc.qblockClient.mu.Lock()
	gotKey := cc.qblockClient.probeGate.key
	state = cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, currentKey, gotKey)
	require.Equal(t, qblockProbeWaiting, state)
	unrelated := newQBlockClientFragment(t, cc, message.Token{0xfe}, 0, true, 64)
	require.True(t, cc.qblockClient.handle(unrelated))
	cc.ReleaseMessage(unrelated)
	cc.qblockClient.mu.Lock()
	gotKey = cc.qblockClient.probeGate.key
	state = cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, currentKey, gotKey)
	require.Equal(t, qblockProbeWaiting, state)

	accepted := newQBlockClientFragment(t, cc, controlToken, 0, true, 64)
	require.True(t, cc.qblockClient.handle(accepted))
	cc.ReleaseMessage(accepted)
	cc.qblockClient.mu.Lock()
	gotKey = cc.qblockClient.probeGate.key
	state = cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, qblockProbeOpen, state)
	require.Zero(t, gotKey)
}

func TestQBlockPacedFeedbackRejectsOldGeneration(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xe2}
	request := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	first := newQBlockClientFragment(t, cc, token, 2, true, 64)
	require.True(t, cc.qblockClient.handle(first))
	cc.ReleaseMessage(first)
	writes := session.writesSnapshot()
	require.Len(t, writes, 2)
	cc.qblockClient.mu.Lock()
	probe := cc.qblockClient.currentProbe
	currentKey := cc.qblockClient.probeGate.key
	if probe != nil {
		probe.generation--
	}
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, probe)
	fragment := newQBlockClientFragment(t, cc, writes[1].token, 0, true, 64)
	require.True(t, cc.qblockClient.handle(fragment))
	cc.ReleaseMessage(fragment)
	cc.qblockClient.mu.Lock()
	gotKey := cc.qblockClient.probeGate.key
	state := cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Equal(t, currentKey, gotKey)
	require.Equal(t, qblockProbeWaiting, state)
}

func TestQBlockPacedPreparationOwnership(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	get := newPrivateQBlockClientGET(t, cc, message.Token{0xf1})
	defer cc.ReleaseMessage(get)
	prepared, err := cc.qblockClient.prepare(get, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	require.True(t, prepared.OwnsTransmission)

	ordinary := newPrivateQBlockClientGET(t, cc, message.Token{0xf2})
	defer cc.ReleaseMessage(ordinary)
	ordinary.SetOptionUint32(message.Block2, 0)
	prepared, err = cc.qblockClient.prepare(ordinary, func(error) {})
	require.NoError(t, err)
	require.False(t, prepared.Prepared)
	require.False(t, prepared.OwnsTransmission)
}

func TestQBlockPacedInitialGETHasSingleWriter(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager:      qblock.DefaultManagerConfig(),
		Clock:        clock,
		ScheduleMode: qblockScheduleManual,
		Pacing:       &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	cc.qblockClient.mu.Lock()
	admitted := cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, now)
	cc.qblockClient.probeGate.charge(1, 20)
	cc.qblockClient.probeGate.settle(1, now)
	cc.qblockClient.mu.Unlock()
	require.True(t, admitted)

	token := message.Token{0xa1}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	type result struct {
		msg *pool.Message
		err error
	}
	done := make(chan result, 1)
	go func() {
		msg, err := cc.doInternal(req)
		done <- result{msg: msg, err: err}
	}()
	require.Eventually(t, func() bool {
		cc.qblockClient.mu.Lock()
		defer cc.qblockClient.mu.Unlock()
		return cc.qblockClient.exchangesByOriginalToken[string(token)] != nil
	}, time.Second, time.Millisecond)
	require.Empty(t, session.writesSnapshot())
	clock.Advance(19 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	require.Empty(t, session.writesSnapshot())
	clock.Advance(time.Second)
	cc.qblockClient.Tick(clock.Now())
	writes := session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, token, writes[0].token)
	require.True(t, writes[0].options.HasOption(message.QBlock2))

	response := newQBlockClientFragment(t, cc, token, 0, false, 16)
	response.SetType(message.NonConfirmable)
	response.SetMessageID(91)
	cc.ProcessReceivedMessageWithHandler(response, cc.handleReq)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.msg)
		cc.ReleaseMessage(got.msg)
	case <-time.After(time.Second):
		t.Fatal("private GET did not complete after its response")
	}
	require.Len(t, session.writesSnapshot(), 1)

	cc.qblockClient.mu.Lock()
	admitted = cc.qblockClient.probeGate.admit(99, qblockProbeControl, 0, clock.Now())
	cc.qblockClient.probeGate.charge(99, 20)
	cc.qblockClient.probeGate.settle(99, clock.Now())
	cc.qblockClient.mu.Unlock()
	require.True(t, admitted)

	queuedContext, cancel := context.WithCancel(context.Background())
	queuedToken := message.Token{0xa2}
	queued := newPrivateQBlockClientGET(t, cc, queuedToken)
	defer cc.ReleaseMessage(queued)
	queued.SetContext(queuedContext)
	queuedDone := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(queued)
		queuedDone <- err
	}()
	require.Eventually(t, func() bool {
		cc.qblockClient.mu.Lock()
		defer cc.qblockClient.mu.Unlock()
		return cc.qblockClient.exchangesByOriginalToken[string(queuedToken)] != nil
	}, time.Second, time.Millisecond)
	require.Len(t, session.writesSnapshot(), 1)
	cancel()
	select {
	case err := <-queuedDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("queued GET did not stop after cancellation")
	}
	clock.Advance(time.Hour)
	cc.qblockClient.Tick(clock.Now())
	require.Len(t, session.writesSnapshot(), 1)
	cc.qblockClient.mu.Lock()
	_, exchangePresent := cc.qblockClient.exchangesByOriginalToken[string(queuedToken)]
	queueLen := len(cc.qblockClient.workQueue.slots)
	cc.qblockClient.mu.Unlock()
	_, handlerPresent := cc.tokenHandlerContainer.Load(queuedToken.Hash())
	cc.qblockClient.callbackSlots.mu.Lock()
	callbacksUsed := cc.qblockClient.callbackSlots.used
	cc.qblockClient.callbackSlots.mu.Unlock()
	require.False(t, exchangePresent)
	require.False(t, handlerPresent)
	require.Zero(t, queueLen)
	require.Zero(t, callbacksUsed)
}

func TestQBlockPacedInitialGETExpiresWithoutWrite(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.Lifetime = 10 * time.Second
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	cc.qblockClient.mu.Lock()
	admitted := cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, now)
	cc.qblockClient.probeGate.charge(1, 20)
	cc.qblockClient.probeGate.settle(1, now)
	cc.qblockClient.mu.Unlock()
	require.True(t, admitted)

	token := message.Token{0xa3}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 1)
	preparation, err := cc.qblockClient.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	require.Empty(t, session.writesSnapshot())
	clock.Advance(10 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	select {
	case err := <-failures:
		require.ErrorIs(t, err, qblock.ErrExpired)
	case <-time.After(time.Second):
		t.Fatal("queued GET did not expire at its lifetime")
	}
	clock.Advance(20 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	require.Empty(t, session.writesSnapshot())
	cc.qblockClient.mu.Lock()
	exchangeCount := len(cc.qblockClient.exchangesByOriginalToken)
	queueLen := len(cc.qblockClient.workQueue.slots)
	cc.qblockClient.mu.Unlock()
	cc.qblockClient.callbackSlots.mu.Lock()
	callbacksUsed := cc.qblockClient.callbackSlots.used
	cc.qblockClient.callbackSlots.mu.Unlock()
	require.Zero(t, exchangeCount)
	require.Zero(t, queueLen)
	require.Zero(t, callbacksUsed)
}

func TestQBlockPacedProbeCorrelationExpiresWithGate(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	request := newPrivateQBlockClientGET(t, cc, message.Token{0xa4})
	defer cc.ReleaseMessage(request)
	preparation, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, preparation.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	cc.qblockClient.mu.Lock()
	probeBefore := cc.qblockClient.currentProbe
	deadline, ok := cc.qblockClient.probeGate.nextDeadline()
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, probeBefore)
	require.True(t, ok)
	clock.Advance(deadline.Sub(now))
	cc.qblockClient.mu.Lock()
	_, _ = cc.qblockClient.nextDeadlineLocked()
	probeAfter := cc.qblockClient.currentProbe
	cc.qblockClient.mu.Unlock()
	require.Nil(t, probeAfter)
}
