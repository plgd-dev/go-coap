package client

import (
	"bytes"
	"context"
	"sync"
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

type fakeQBlockClock struct {
	mu         sync.Mutex
	now        time.Time
	newTimers  uint32
	timer      *fakeQBlockTimer
	onNewTimer func()
}

func newFakeQBlockClock(now time.Time) *fakeQBlockClock { return &fakeQBlockClock{now: now} }
func (c *fakeQBlockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeQBlockClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	if c.timer != nil && c.timer.active && !c.now.Before(c.timer.deadline) {
		c.timer.active = false
		select {
		case c.timer.ch <- c.now:
		default:
		}
	}
	c.mu.Unlock()
}
func (c *fakeQBlockClock) NewTimer() qblockTimer {
	if c.onNewTimer != nil {
		c.onNewTimer()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.newTimers++
	c.timer = &fakeQBlockTimer{clock: c, ch: make(chan time.Time, 1)}
	return c.timer
}

func TestQBlockSchedulerStartsAfterConnectionHooks(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	hooksReady := false
	clock.onNewTimer = func() { hooksReady = len(session.onClose) > 0 }
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	require.NotNil(t, cc.qblockClient.scheduler)
	require.True(t, hooksReady, "scheduler timer was created before the connection close hook")
}

func (c *fakeQBlockClock) timerCreated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timer != nil
}

func (c *fakeQBlockClock) activeTimer() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timer != nil && c.timer.active
}

func (c *fakeQBlockClock) deadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer == nil {
		return time.Time{}
	}
	return c.timer.deadline
}

func (c *fakeQBlockClock) deliverStale() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer == nil {
		return
	}
	select {
	case c.timer.ch <- c.now:
	default:
	}
}

type fakeQBlockTimer struct {
	clock    *fakeQBlockClock
	ch       chan time.Time
	active   bool
	deadline time.Time
	stops    uint32
}

func (c *fakeQBlockClock) stopCount() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer == nil {
		return 0
	}
	return c.timer.stops
}

func (t *fakeQBlockTimer) C() <-chan time.Time { return t.ch }
func (t *fakeQBlockTimer) Reset(d time.Duration) {
	t.clock.mu.Lock()
	t.active = true
	t.deadline = t.clock.now.Add(d)
	t.clock.mu.Unlock()
}
func (t *fakeQBlockTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	t.stops++
	active := t.active
	t.active = false
	return active
}

func TestQBlockManualModeDoesNotCreateTimer(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newQBlockClockTestConn(t, qblockClientConfig{
		Manager:      qblock.DefaultManagerConfig(),
		Clock:        clock,
		ScheduleMode: qblockScheduleManual,
	})

	require.Zero(t, clock.newTimers)
	cc.qblockClient.Tick(clock.Now())
	require.Zero(t, clock.newTimers)
}

func TestQBlockAutomaticModeRejectsMixedClockConfiguration(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newQBlockClockTestConn(t, qblockClientConfig{
		Manager:      qblock.DefaultManagerConfig(),
		Clock:        clock,
		Now:          time.Now,
		ScheduleMode: qblockScheduleAutomatic,
	})

	require.Error(t, cc.qblockClient.initErr)
}

func TestQBlockCallbackSlotsBoundAndRecoverCapacity(t *testing.T) {
	slots := newQBlockCallbackSlots(1)
	release, ok := slots.tryAcquire()
	require.True(t, ok)
	_, ok = slots.tryAcquire()
	require.False(t, ok)

	release()
	release()
	release, ok = slots.tryAcquire()
	require.True(t, ok)
	release()
}

func TestQBlockCallbackDispatcherStopDoesNotWaitForBlockedCallback(t *testing.T) {
	dispatcher := newQBlockCallbackDispatcher(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	require.True(t, dispatcher.submit(func() {
		close(entered)
		<-release
	}))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback dispatcher did not start callback")
	}

	stopped := make(chan struct{})
	go func() {
		dispatcher.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("callback dispatcher stop waited for callback")
	}

	close(release)
	select {
	case <-dispatcher.stopped:
	case <-time.After(time.Second):
		t.Fatal("callback dispatcher did not stop after callback returned")
	}
}

func TestQBlockCallbackDispatcherDiscardsQueuedCallbackOnStop(t *testing.T) {
	dispatcher := newQBlockCallbackDispatcher(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	slots := newQBlockCallbackSlots(1)
	releaseSlot, ok := slots.tryAcquire()
	require.True(t, ok)
	require.True(t, dispatcher.submit(func() {
		close(entered)
		<-release
	}))
	<-entered
	ran := make(chan struct{})
	discarded := make(chan struct{})
	require.True(t, dispatcher.submitWithDiscard(func() { close(ran) }, func() {
		releaseSlot()
		close(discarded)
	}))
	stopped := make(chan struct{})
	go func() {
		dispatcher.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("dispatcher stop waited for the blocked callback")
	}
	select {
	case <-discarded:
	case <-time.After(time.Second):
		t.Fatal("queued callback was not discarded")
	}
	slots.mu.Lock()
	used := slots.used
	slots.mu.Unlock()
	require.Zero(t, used)
	select {
	case <-ran:
		t.Fatal("discarded callback ran")
	default:
	}
	close(release)
	<-dispatcher.stopped
}

func TestQBlockSchedulerArmsEarliestDeadlineAndParksIdle(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newAutomaticQBlockClockTestConn(t, clock)
	require.Eventually(t, clock.timerCreated, time.Second, time.Millisecond)
	require.False(t, clock.activeTimer())

	startQBlockSchedulerQ1(t, cc, message.Token{0x01}, []byte("upload"))
	cc.qblockClient.mu.Lock()
	deadline, ok := cc.qblockClient.nextDeadlineLocked()
	cc.qblockClient.mu.Unlock()
	require.True(t, ok)
	require.Eventually(t, func() bool {
		return clock.activeTimer() && clock.deadline().Equal(deadline)
	}, time.Second, time.Millisecond)

	cc.qblockClient.abandon(message.Token{0x01}, qblock.ErrCanceled)
	require.Eventually(t, func() bool { return !clock.activeTimer() }, time.Second, time.Millisecond)
}

func TestQBlockSchedulerStaleWakeDoesNotDuplicateBurst(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	startQBlockSchedulerQ1(t, cc, message.Token{0x02}, bytes.Repeat([]byte("x"), 48))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	writes := len(session.writesSnapshot())
	deadline := clock.deadline()

	clock.deliverStale()
	require.Eventually(t, func() bool { return clock.deadline().Equal(deadline) }, time.Second, time.Millisecond)
	require.Len(t, session.writesSnapshot(), writes)
}

func TestQBlockSchedulerArmsAfterInboundReceiverStart(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newAutomaticQBlockClockTestConn(t, clock)
	request := newPrivateQBlockClientGET(t, cc, message.Token{0x03})
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	require.False(t, clock.activeTimer())

	response := newQBlockClientResponse(t, cc, request.Token(), true)
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
}

func TestQBlockSchedulerAdvancesReceiverWithoutManualTick(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	request := newPrivateQBlockClientGET(t, cc, message.Token{0x04})
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	response := newQBlockClientResponse(t, cc, request.Token(), true)
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	writesBefore := len(session.writesSnapshot())
	clock.Advance(clock.deadline().Sub(clock.Now()))
	require.Eventually(t, func() bool {
		return len(session.writesSnapshot()) > writesBefore
	}, time.Second, time.Millisecond)
	last := session.writesSnapshot()[len(session.writesSnapshot())-1]
	require.Equal(t, message.NonConfirmable, last.typ)
	require.True(t, last.options.HasOption(message.QBlock2))
}

func TestQBlockAutomaticTickDoesNotDuplicateScheduledAdvance(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	request := newPrivateQBlockClientGET(t, cc, message.Token{0x06})
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	response := newQBlockClientResponse(t, cc, request.Token(), true)
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	writes := len(session.writesSnapshot())
	cc.CheckExpirations(clock.deadline())
	require.Len(t, session.writesSnapshot(), writes, "legacy expiration check must not advance automatic Q-Block state")
	clock.Advance(clock.deadline().Sub(clock.Now()))
	require.Eventually(t, func() bool { return len(session.writesSnapshot()) > writes }, time.Second, time.Millisecond)
	writes = len(session.writesSnapshot())
	cc.CheckExpirations(clock.Now())
	require.Len(t, session.writesSnapshot(), writes, "legacy expiration check must not repeat the due burst")
}

func TestQBlockCloseStopsSchedulerBeforeBlockedWriteDrains(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	request := newPrivateQBlockClientGET(t, cc, message.Token{0x07})
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	response := newQBlockClientResponse(t, cc, request.Token(), true)
	defer cc.ReleaseMessage(response)
	require.True(t, cc.qblockClient.handle(response))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	session.contextWriteStart = make(chan struct{}, 1)
	session.releaseContextWrite = make(chan struct{})
	clock.Advance(clock.deadline().Sub(clock.Now()))
	select {
	case <-session.contextWriteStart:
	case <-time.After(time.Second):
		t.Fatal("scheduled Q2 control write did not start")
	}
	closed := make(chan struct{})
	go func() {
		session.closeForTest()
		close(closed)
	}()
	requireQBlockCompletion(t, cc.qblockClient.schedulerStopped(), "scheduler owner stop")
	requireQBlockCompletion(t, closed, "close after scheduled write cancellation")
	requireQBlockClientEmpty(t, cc)
}

func TestQBlockSchedulerKeepsOneDueTurnWhileWorkerWaitsForGate(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	cc := newAutomaticQBlockClockTestConn(t, clock)
	request := newPrivateQBlockClientGET(t, cc, message.Token{0x05})
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockClientResponse(t, cc, request.Token(), true)
	defer cc.ReleaseMessage(first)
	require.True(t, cc.qblockClient.handle(first))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)

	contended := make(chan struct{}, 1)
	cc.qblockClient.actionMuContention = func() { contended <- struct{}{} }
	cc.qblockClient.actionMu.Lock()
	locked := true
	defer func() {
		if locked {
			cc.qblockClient.actionMu.Unlock()
		}
	}()
	clock.Advance(clock.deadline().Sub(clock.Now()))
	requireQBlockContention(t, contended, "scheduler due worker")
	stopsBeforeNotify := clock.stopCount()
	cc.qblockClient.notifyDeadlineChanged()
	require.Eventually(t, func() bool {
		return clock.stopCount() > stopsBeforeNotify
	}, time.Second, time.Millisecond)
	require.Empty(t, cc.qblockClient.scheduler.due, "worker already owns the one due turn")
	cc.qblockClient.actionMu.Unlock()
	locked = false
}

func TestQBlockSchedulerSamplesTimeAfterWaitingForGate(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	session := &qblockTestSession{ctx: context.Background()}
	cc := newAutomaticQBlockClockTestConnWithSession(t, clock, session)
	startQBlockSchedulerQ1(t, cc, message.Token{0x31}, bytes.Repeat([]byte("x"), 320))
	require.Eventually(t, clock.activeTimer, time.Second, time.Millisecond)
	writes := len(session.writesSnapshot())

	contended := make(chan struct{}, 1)
	cc.qblockClient.actionMuContention = func() { contended <- struct{}{} }
	cc.qblockClient.actionMu.Lock()
	locked := true
	defer func() {
		if locked {
			cc.qblockClient.actionMu.Unlock()
		}
	}()
	clock.Advance(clock.deadline().Sub(clock.Now()))
	requireQBlockContention(t, contended, "scheduler due worker")
	clock.Advance(qblock.DefaultManagerConfig().Transfer.Lifetime)
	cc.qblockClient.actionMu.Unlock()
	locked = false
	require.Eventually(t, func() bool { return cc.qblockClient.active() == 0 }, time.Second, time.Millisecond)
	require.Len(t, session.writesSnapshot(), writes, "a transfer expired while waiting for the gate must not send")
}

func TestQBlockSchedulerRearmsServerRetentionAfterReset(t *testing.T) {
	clock := newFakeQBlockClock(time.Unix(100, 0))
	h := &serverHarness{now: clock.Now(), nextMID: 1}
	h.session = &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 32))))
	}
	cfg.GetMID = func() int32 {
		mid := h.nextMID
		h.nextMID++
		return mid
	}
	h.cc = NewConnWithOpts(h.session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: clock, ScheduleMode: qblockScheduleAutomatic}),
		withQBlockServer(qblockServerConfig{Retention: 2 * qblock.DefaultManagerConfig().Transfer.Lifetime}),
	)
	t.Cleanup(h.session.closeForTest)
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	h.cc.qblockClient.mu.Lock()
	initialDeadline, initialOK := h.cc.qblockClient.nextDeadlineLocked()
	h.cc.qblockClient.mu.Unlock()
	require.True(t, initialOK)
	require.Eventually(t, func() bool {
		return clock.activeTimer() && clock.deadline().Equal(initialDeadline)
	}, time.Second, time.Millisecond)
	writes := h.session.writesSnapshot()
	require.NotEmpty(t, writes)
	reset := h.cc.AcquireMessage(context.Background())
	defer h.cc.ReleaseMessage(reset)
	reset.SetType(message.Reset)
	reset.SetMessageID(writes[0].mid)
	require.True(t, h.cc.qblockClient.handle(reset))
	h.cc.qblockClient.mu.Lock()
	deadline, ok := h.cc.qblockClient.server.nextRecordDeadlineLocked()
	h.cc.qblockClient.mu.Unlock()
	require.True(t, ok)
	require.NotEqual(t, initialDeadline, deadline)
	require.Eventually(t, func() bool {
		return clock.activeTimer() && clock.deadline().Equal(deadline)
	}, time.Second, time.Millisecond)
	clock.Advance(deadline.Sub(clock.Now()))
	require.Eventually(t, func() bool { return h.snapshot().records == 0 }, time.Second, time.Millisecond)
}

func newQBlockClockTestConn(t *testing.T, qblockConfig qblockClientConfig) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	session := &qblockTestSession{ctx: context.Background()}
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockConfig))
	t.Cleanup(session.closeForTest)
	return cc
}

func newAutomaticQBlockClockTestConn(t *testing.T, clock *fakeQBlockClock) *Conn {
	t.Helper()
	return newAutomaticQBlockClockTestConnWithSession(t, clock, &qblockTestSession{ctx: context.Background()})
}

func newAutomaticQBlockClockTestConnWithSession(t *testing.T, clock *fakeQBlockClock, session *qblockTestSession) *Conn {
	t.Helper()
	return newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager:      qblock.DefaultManagerConfig(),
		Clock:        clock,
		ScheduleMode: qblockScheduleAutomatic,
	})
}

func newQBlockClockTestConnWithSession(t *testing.T, session *qblockTestSession, qblockConfig qblockClientConfig) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockConfig))
	t.Cleanup(session.closeForTest)
	return cc
}

func startQBlockSchedulerQ1(t *testing.T, cc *Conn, token message.Token, body []byte) {
	t.Helper()
	request := newPOSTWithBody(t, cc, token, body)
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
}
