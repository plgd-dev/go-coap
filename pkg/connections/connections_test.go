package connections

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
	"github.com/stretchr/testify/require"
)

type testConn struct {
	remote net.Addr
	checks atomic.Int32
	check  func()
}

func (c *testConn) Context() context.Context { return context.Background() }
func (c *testConn) Close() error             { return nil }
func (c *testConn) RemoteAddr() net.Addr     { return c.remote }
func (c *testConn) CheckExpirations(time.Time) {
	c.checks.Add(1)
	c.check()
}

func TestCheckExpirationsDoesNotWaitForSlowConnections(t *testing.T) {
	c := New()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	slowDone := make(chan struct{})
	slowStarted := make(chan struct{})
	slow := &testConn{
		remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		check: func() {
			close(slowStarted)
			defer close(slowDone)
			<-release
		},
	}
	c.Store(slow)

	checked := make(chan struct{}, 2)
	fast := &testConn{
		remote: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2},
		check:  func() { checked <- struct{}{} },
	}
	c.Store(fast)
	fastValue, ok := c.data.Load(fast.remote.String())
	require.True(t, ok)
	fastMonitor := fastValue.(*monitored)

	for i := range 2 {
		returned := make(chan struct{})
		go func() {
			c.CheckExpirations(time.Now())
			close(returned)
		}()

		for _, done := range []<-chan struct{}{returned, checked} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("expiration check blocked on another connection")
			}
		}
		require.Eventually(t, func() bool {
			return !fastMonitor.checking.Load()
		}, time.Second, time.Millisecond, "responsive connection check did not finish")
		if i == 0 {
			select {
			case <-slowStarted:
			case <-time.After(time.Second):
				t.Fatal("slow expiration check did not start")
			}
		}
	}

	require.EqualValues(t, 1, slow.checks.Load(), "a pending check must not be started again")

	unblock()
	select {
	case <-slowDone:
	case <-time.After(time.Second):
		t.Fatal("slow expiration check did not finish")
	}
}
