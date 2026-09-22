package client

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

type fakeQBlockClock struct {
	now       time.Time
	newTimers uint32
}

func newFakeQBlockClock(now time.Time) *fakeQBlockClock { return &fakeQBlockClock{now: now} }
func (c *fakeQBlockClock) Now() time.Time               { return c.now }
func (c *fakeQBlockClock) Advance(d time.Duration)      { c.now = c.now.Add(d) }
func (c *fakeQBlockClock) NewTimer() qblockTimer {
	c.newTimers++
	return &fakeQBlockTimer{ch: make(chan time.Time, 1)}
}

type fakeQBlockTimer struct{ ch chan time.Time }

func (t *fakeQBlockTimer) C() <-chan time.Time { return t.ch }
func (*fakeQBlockTimer) Reset(time.Duration)   {}
func (*fakeQBlockTimer) Stop() bool            { return true }

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
