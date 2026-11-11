package client

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapSync "github.com/plgd-dev/go-coap/v3/pkg/sync"
	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
	"github.com/stretchr/testify/require"
)

func TestMidElementRetransmitExponentialBackoff(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 100 * time.Millisecond,
	}

	require.False(t, m.Retransmit(start.Add(100*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(101*time.Millisecond)))
	m.MarkRetransmitted(start.Add(101 * time.Millisecond))
	require.False(t, m.Retransmit(start.Add(300*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(301*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(302*time.Millisecond)))
	m.MarkRetransmitted(start.Add(302 * time.Millisecond))
	require.False(t, m.Retransmit(start.Add(701*time.Millisecond)))
	require.False(t, m.Retransmit(start.Add(702*time.Millisecond)))
	require.True(t, m.Retransmit(start.Add(703*time.Millisecond)))
}

func TestMidElementRetransmitDelayedExpirationCheck(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 2500 * time.Millisecond,
	}

	retransmittedAt := start.Add(4 * time.Second)
	require.True(t, m.Retransmit(retransmittedAt))
	m.MarkRetransmitted(retransmittedAt)
	require.False(t, m.Retransmit(start.Add(8*time.Second)))
	require.False(t, m.Retransmit(start.Add(9*time.Second)))
	require.True(t, m.Retransmit(start.Add(9*time.Second+time.Millisecond)))
}

func TestMidElementExpiresAfterFinalAcknowledgmentWait(t *testing.T) {
	start := time.Now()
	m := midElement{
		start:              start,
		acknowledgeTimeout: 2500 * time.Millisecond,
	}
	m.retransmit.Store(4)

	finalAckDeadline := start.Add(40 * time.Second)
	require.False(t, m.IsExpired(finalAckDeadline.Add(-time.Nanosecond), 4))
	require.False(t, m.IsExpired(finalAckDeadline, 4))
	require.True(t, m.IsExpired(finalAckDeadline.Add(time.Nanosecond), 4))
}

func TestMidElementFailedRetransmissionDoesNotAdvanceBackoff(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	session.setWriteError(context.DeadlineExceeded)
	msg := cc.AcquireMessage(context.Background())
	const mid = 1
	element := &midElement{
		start:              time.Now().Add(-time.Second),
		acknowledgeTimeout: 100 * time.Millisecond,
		private: struct {
			sync.Mutex
			msg *pool.Message
		}{msg: msg},
	}
	cc.midHandlerContainer.Store(mid, element)
	defer func() {
		cc.midHandlerContainer.Delete(mid)
		element.ReleaseMessage(cc)
	}()

	now := time.Now()
	cc.checkMidHandlerContainer(now, 4, mid, element)
	require.Zero(t, element.retransmit.Load())
	require.False(t, element.retransmitting)

	session.setWriteError(nil)
	cc.checkMidHandlerContainer(now, 4, mid, element)
	require.Equal(t, uint32(1), element.retransmit.Load())
}

func TestRetransmissionFailureDoesNotOverrideAcknowledgment(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	session.setWriteError(context.DeadlineExceeded)
	msg := cc.AcquireMessage(context.Background())
	const mid = 1
	waitForAcknowledge := make(chan error, 1)
	element := &midElement{
		start:              time.Now().Add(-time.Second),
		acknowledgeTimeout: 100 * time.Millisecond,
		waitForAcknowledge: waitForAcknowledge,
		private: struct {
			sync.Mutex
			msg *pool.Message
		}{msg: msg},
	}
	cc.midHandlerContainer.Store(mid, element)
	session.writeErrorHook = func() {
		if acknowledged, ok := cc.midHandlerContainer.LoadAndDelete(mid); ok {
			acknowledged.ReleaseMessage(cc)
			notifyWaitForAcknowledge(acknowledged.waitForAcknowledge, nil)
		}
	}

	cc.checkMidHandlerContainer(time.Now(), 4, mid, element)

	require.NoError(t, <-waitForAcknowledge)
	_, ok := cc.midHandlerContainer.Load(mid)
	require.False(t, ok)
	element.private.Lock()
	require.Nil(t, element.private.msg)
	element.private.Unlock()
}

func TestMidElementRepeatedRetransmissionFailuresAreBounded(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	session.setWriteError(context.DeadlineExceeded)
	msg := cc.AcquireMessage(context.Background())
	const (
		mid           = 1
		maxRetransmit = 2
	)
	waitForAcknowledge := make(chan error, 1)
	element := &midElement{
		start:              time.Now().Add(-time.Second),
		acknowledgeTimeout: 100 * time.Millisecond,
		waitForAcknowledge: waitForAcknowledge,
		private: struct {
			sync.Mutex
			msg *pool.Message
		}{msg: msg},
	}
	cc.midHandlerContainer.Store(mid, element)

	now := time.Now()
	for range maxRetransmit {
		cc.checkMidHandlerContainer(now, maxRetransmit, mid, element)
	}

	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	require.ErrorIs(t, cc.waitForAcknowledge(req, waitForAcknowledge), context.DeadlineExceeded)

	_, ok := cc.midHandlerContainer.Load(mid)
	require.False(t, ok)
	require.Zero(t, element.retransmit.Load())
	element.private.Lock()
	require.Nil(t, element.private.msg)
	element.private.Unlock()
}

type inactivityMonitorRecorder struct {
	checks int
}

func (m *inactivityMonitorRecorder) Notify() {}

func (m *inactivityMonitorRecorder) CheckInactivity(time.Time, *Conn) {
	m.checks++
}

func TestInactivityDeferredWhileConfirmableMessagesPending(t *testing.T) {
	monitor := &inactivityMonitorRecorder{}
	cc := Conn{midHandlerContainer: coapSync.NewMap[int32, *midElement]()}
	wrapped := DeferInactivityWhileConfirmableMessagesPending(monitor)

	cc.midHandlerContainer.Store(1, &midElement{})
	wrapped.CheckInactivity(time.Now(), &cc)
	require.Zero(t, monitor.checks)

	cc.midHandlerContainer.Delete(1)
	wrapped.CheckInactivity(time.Now(), &cc)
	require.Equal(t, 1, monitor.checks)
}

func TestTransmissionNextAcknowledgeTimeoutRandomFactor(t *testing.T) {
	transmission := Transmission{
		acknowledgeTimeout:      atomic.NewDuration(100 * time.Millisecond),
		acknowledgeRandomFactor: atomic.NewFloat64(1.5),
	}

	for range 100 {
		timeout := transmission.nextAcknowledgeTimeout()
		require.GreaterOrEqual(t, timeout, 100*time.Millisecond)
		require.Less(t, timeout, 150*time.Millisecond)
	}
}

func TestTransmissionNextAcknowledgeTimeoutInvalidFactor(t *testing.T) {
	for _, factor := range []float64{math.NaN(), math.Inf(1)} {
		transmission := Transmission{
			acknowledgeTimeout:      atomic.NewDuration(100 * time.Millisecond),
			acknowledgeRandomFactor: atomic.NewFloat64(factor),
		}

		require.Equal(t, 100*time.Millisecond, transmission.nextAcknowledgeTimeout())
	}
}

func TestTransmissionNextAcknowledgeTimeoutDurationLimit(t *testing.T) {
	transmission := Transmission{
		acknowledgeTimeout:      atomic.NewDuration(time.Duration(math.MaxInt64)),
		acknowledgeRandomFactor: atomic.NewFloat64(2),
	}

	require.Equal(t, time.Duration(math.MaxInt64), transmission.nextAcknowledgeTimeout())
}
