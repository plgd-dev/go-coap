package client

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

type fakeQBlockClock struct {
	mu        sync.Mutex
	now       time.Time
	newTimers uint32
	timer     *fakeQBlockTimer
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
	c.mu.Unlock()
}
func (c *fakeQBlockClock) NewTimer() qblockTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.newTimers++
	c.timer = &fakeQBlockTimer{clock: c, ch: make(chan time.Time, 1)}
	return c.timer
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
