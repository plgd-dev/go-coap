package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestQBlockPacingCancelQueuedGETCallsFailureOnce(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	cc.qblockClient.mu.Lock()
	require.True(t, cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, now))
	cc.qblockClient.probeGate.charge(1, 20)
	cc.qblockClient.probeGate.settle(1, now)
	cc.qblockClient.mu.Unlock()

	token := message.Token{0xa5}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared.OwnsTransmission)
	require.Empty(t, session.writesSnapshot())

	cc.qblockClient.abandon(token, qblock.ErrCanceled)
	select {
	case failure := <-failures:
		require.ErrorIs(t, failure, qblock.ErrCanceled)
	case <-time.After(time.Second):
		t.Fatal("queued GET cancellation did not report failure")
	}
	cc.qblockClient.abandon(token, qblock.ErrCanceled)
	select {
	case failure := <-failures:
		t.Fatalf("duplicate terminal callback: %v", failure)
	default:
	}
	clock.Advance(20 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	require.Empty(t, session.writesSnapshot())
	cc.qblockClient.mu.Lock()
	require.Empty(t, cc.qblockClient.workQueue.slots)
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	cc.qblockClient.mu.Unlock()
}

func TestQBlockPacingSchedulerNoBusyLoop(t *testing.T) {
	config := qblock.DefaultManagerConfig()
	h := newServerHarness(t, config, qblockServerConfig{}, nil)
	start := h.now
	h.cc.qblockClient.mu.Lock()
	admitted := h.cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, start)
	h.cc.qblockClient.probeGate.charge(1, 20)
	h.cc.qblockClient.probeGate.settle(1, start)
	h.cc.qblockClient.mu.Unlock()
	require.True(t, admitted)
	h.ingest(h.q1(t, 0xb1, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(config.Transfer.NonReceiveTimeout)
	h.cc.qblockClient.mu.Lock()
	deadline, hasDeadline := h.cc.qblockClient.nextDeadlineLocked()
	var pending []qblock.ControlIntent
	for _, record := range h.cc.qblockClient.server.byID {
		pending = h.cc.qblockClient.manager.PendingControls(record.id)
	}
	h.cc.qblockClient.mu.Unlock()
	require.Len(t, pending, 1)
	require.True(t, hasDeadline)
	require.Equal(t, start.Add(20*time.Second), deadline)
	require.Empty(t, h.session.writesSnapshot())
	h.advance(time.Second)
	require.Empty(t, h.session.writesSnapshot(), "an old retry deadline must not trigger a speculative report")
}

func TestQBlockPacingLateCommitAfterReuse(t *testing.T) {
	config := qblock.DefaultManagerConfig()
	h := newServerHarness(t, config, qblockServerConfig{}, nil)
	h.cc.qblockClient.mu.Lock()
	admitted := h.cc.qblockClient.probeGate.admit(1, qblockProbeBody, time.Hour, h.now)
	h.cc.qblockClient.mu.Unlock()
	require.True(t, admitted)
	h.ingest(h.q1(t, 0xb4, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(config.Transfer.NonReceiveTimeout)
	h.cc.qblockClient.mu.Lock()
	var oldID qblock.TransferID
	var oldRevision uint64
	for _, record := range h.cc.qblockClient.server.byID {
		oldID = record.id
		intents := h.cc.qblockClient.manager.PendingControls(record.id)
		if len(intents) != 0 {
			oldRevision = intents[0].Revision
		}
	}
	h.cc.qblockClient.mu.Unlock()
	require.NotZero(t, oldID)
	require.NotZero(t, oldRevision)
	h.advance(config.Transfer.Lifetime - config.Transfer.NonReceiveTimeout)
	require.Zero(t, h.snapshot().records)

	h.ingest(h.q1(t, 0xb5, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(config.Transfer.NonReceiveTimeout)
	h.cc.qblockClient.mu.Lock()
	var newID qblock.TransferID
	var before []qblock.ControlIntent
	for _, record := range h.cc.qblockClient.server.byID {
		newID = record.id
		before = h.cc.qblockClient.manager.PendingControls(record.id)
	}
	late := h.cc.qblockClient.manager.CommitControl(oldID, oldRevision, h.now)
	var after []qblock.ControlIntent
	if newID != 0 {
		after = h.cc.qblockClient.manager.PendingControls(newID)
	}
	h.cc.qblockClient.mu.Unlock()
	require.NotZero(t, newID)
	require.NotEqual(t, oldID, newID)
	require.Len(t, before, 1)
	require.Empty(t, late)
	require.Equal(t, before, after)
}

func TestQBlockPacingCloseInterruptsBothRoles(t *testing.T) {
	config := qblock.DefaultManagerConfig()
	handlerCalls := 0
	h := newServerHarness(t, config, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
	})
	start := h.now
	h.cc.qblockClient.mu.Lock()
	admitted := h.cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, start)
	h.cc.qblockClient.probeGate.charge(1, 10)
	h.cc.qblockClient.probeGate.settle(1, start)
	h.cc.qblockClient.mu.Unlock()
	require.True(t, admitted)
	failures := make(chan error, 2)
	get := newPrivateQBlockClientGET(t, h.cc, message.Token{0xb2})
	defer h.cc.ReleaseMessage(get)
	prepared, err := h.cc.qblockClient.prepare(get, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared.OwnsTransmission)
	h.ingest(h.q1(t, 0xb3, 0, false, 4, "body"))
	require.Equal(t, 1, handlerCalls)
	require.Empty(t, h.session.writesSnapshot())
	h.cc.qblockClient.mu.Lock()
	slotsBefore := len(h.cc.qblockClient.workQueue.slots)
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, 2, slotsBefore)

	h.session.contextWriteStart = make(chan struct{}, 1)
	h.session.releaseContextWrite = make(chan struct{})
	h.now = h.now.Add(10 * time.Second)
	tickDone := make(chan struct{})
	go func() {
		h.cc.CheckExpirations(h.now)
		close(tickDone)
	}()
	select {
	case <-h.session.contextWriteStart:
	case <-time.After(time.Second):
		t.Fatal("ready writer did not start")
	}
	closeDone := make(chan struct{})
	go func() {
		h.cc.qblockClient.close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel a blocked write")
	}
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("blocked output turn did not drain after close")
	}
	select {
	case failure := <-failures:
		require.ErrorIs(t, failure, qblock.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("queued client callback was not reported on close")
	}
	h.cc.qblockClient.mu.Lock()
	slotsAfter := len(h.cc.qblockClient.workQueue.slots)
	h.cc.qblockClient.mu.Unlock()
	require.Zero(t, slotsAfter)
	require.Empty(t, h.session.writesSnapshot())
}

func TestQBlockPacingSchedulerExpiryWinsTie(t *testing.T) {
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
	require.True(t, cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, now))
	cc.qblockClient.probeGate.charge(1, 10)
	cc.qblockClient.probeGate.settle(1, now)
	cc.qblockClient.mu.Unlock()

	token := message.Token{0xa6}
	req := newPOSTWithBody(t, cc, token, bytes.Repeat([]byte{'u'}, 48))
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared.OwnsTransmission)
	require.Empty(t, session.writesSnapshot())
	clock.Advance(10 * time.Second)
	cc.qblockClient.Tick(clock.Now())
	select {
	case failure := <-failures:
		require.ErrorIs(t, failure, qblock.ErrExpired)
	case <-time.After(time.Second):
		t.Fatal("prepared upload did not expire at readiness tie")
	}
	require.Empty(t, session.writesSnapshot())
	require.Zero(t, cc.qblockClient.active())
	cc.qblockClient.mu.Lock()
	require.Empty(t, cc.qblockClient.workQueue.slots)
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	cc.qblockClient.mu.Unlock()
	cc.qblockClient.Tick(clock.Now())
	select {
	case failure := <-failures:
		t.Fatalf("duplicate expiry callback: %v", failure)
	default:
	}
}

func TestQBlockPacingSchedulerStaleWakeAfterClose(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	startQBlockSchedulerQ1(t, cc, message.Token{0xa9}, bytes.Repeat([]byte{'u'}, 48))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	cc.qblockClient.close()
	select {
	case <-cc.qblockClient.schedulerStopped():
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	writes := session.writesSnapshot()
	clock.deliverStale()
	require.False(t, clock.activeTimer())
	require.Equal(t, writes, session.writesSnapshot())
}

func TestQBlockPacingCallbackReentersClose(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	cc.qblockClient.mu.Lock()
	require.True(t, cc.qblockClient.probeGate.admit(1, qblockProbeControl, 0, now))
	cc.qblockClient.probeGate.charge(1, 20)
	cc.qblockClient.probeGate.settle(1, now)
	cc.qblockClient.mu.Unlock()
	token := message.Token{0xa7}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	called := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) {
		cc.qblockClient.close()
		called <- err
	})
	require.NoError(t, err)
	require.True(t, prepared.OwnsTransmission)
	done := make(chan struct{})
	go func() {
		cc.qblockClient.abandon(token, qblock.ErrCanceled)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal callback deadlocked while closing its connection")
	}
	require.ErrorIs(t, <-called, qblock.ErrCanceled)
	select {
	case failure := <-called:
		t.Fatalf("duplicate callback after close: %v", failure)
	default:
	}
}

func TestQBlockPacingCancelPartialBatch(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xa8}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	require.True(t, prepared.OwnsTransmission)
	cc.qblockClient.drivePending(now)
	first := newQBlockClientFragment(t, cc, token, 2, true, 64)
	defer cc.ReleaseMessage(first)
	require.True(t, cc.qblockClient.handle(first))
	writes := session.writesSnapshot()
	require.Len(t, writes, 2)
	cc.qblockClient.mu.Lock()
	transfer := cc.qblockClient.transferByToken[string(token)]
	var pending []qblock.ControlIntent
	if transfer != nil {
		pending = cc.qblockClient.manager.PendingControls(transfer.id)
	}
	debt := cc.qblockClient.probeGate.bytes
	cc.qblockClient.mu.Unlock()
	require.NotNil(t, transfer)
	require.Len(t, pending, 1)
	require.Positive(t, debt)

	cc.qblockClient.abandon(token, qblock.ErrCanceled)
	cc.qblockClient.mu.Lock()
	remainingSlots := len(cc.qblockClient.workQueue.slots)
	gateState := cc.qblockClient.probeGate.state
	cc.qblockClient.mu.Unlock()
	require.Zero(t, remainingSlots)
	require.Equal(t, qblockProbeWaiting, gateState, "attempted packet keeps its debt")
	clock.Advance(time.Minute)
	cc.qblockClient.Tick(clock.Now())
	require.Len(t, session.writesSnapshot(), 2, "cancellation must not emit the second repair")
	require.Zero(t, cc.qblockClient.active())
}
