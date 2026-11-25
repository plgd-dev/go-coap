package handshake

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testConn struct {
	net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	handshake func(context.Context) error
}

func newTestConn(t *testing.T, fn func(context.Context) error) *testConn {
	t.Helper()
	peer, conn := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	c := &testConn{Conn: conn, closed: make(chan struct{}), handshake: fn}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (c *testConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.Conn.Close()
	})
	return err
}

func (c *testConn) HandshakeContext(ctx context.Context) error {
	return c.handshake(ctx)
}

func readResult[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for result")
		var zero T
		return zero
	}
}

func TestCanceledHandshakeCannotSucceed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pending := NewPending(1)
	conn := newTestConn(t, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	attempt := pending.Admit(ctx, conn)
	require.NotNil(t, attempt)
	cancel()
	err := attempt.Run(0)
	require.ErrorIs(t, err, context.Canceled)
	_, err = conn.Write([]byte{1})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Nil(t, pending.Admit(ctx, conn))
}

func TestTimedOutHandshakeCannotSucceed(t *testing.T) {
	pending := NewPending(1)
	conn := newTestConn(t, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	attempt := pending.Admit(context.Background(), conn)
	require.NotNil(t, attempt)
	require.ErrorIs(t, attempt.Run(time.Millisecond), context.DeadlineExceeded)
	select {
	case <-conn.closed:
	default:
		t.Fatal("timed-out connection was not closed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	replacement := newTestConn(t, func(context.Context) error { return nil })
	next := pending.Admit(ctx, replacement)
	require.NotNil(t, next, "timed-out handshake did not release its slot")
	require.NoError(t, next.Run(0))
}

func TestNilAttempt(t *testing.T) {
	var attempt *Attempt
	require.ErrorContains(t, attempt.Run(0), "nil handshake attempt")
}

func TestEvictionClosesStalledConnectionAndPreservesError(t *testing.T) {
	pending := NewPending(1)
	started := make(chan struct{})
	originalErr := errors.New("underlying handshake failure")
	conn := newTestConn(t, nil)
	conn.handshake = func(context.Context) error {
		close(started)
		<-conn.closed
		return originalErr
	}
	first := pending.Admit(context.Background(), conn)
	result := make(chan error, 1)
	go func() { result <- first.Run(0) }()
	<-started
	replacement := newTestConn(t, func(context.Context) error { return nil })
	next := pending.Admit(context.Background(), replacement)
	require.NotNil(t, next)
	err := readResult(t, result)
	require.ErrorIs(t, err, ErrTooManyPending)
	require.ErrorContains(t, err, originalErr.Error())
	require.NoError(t, next.Run(0))
}

func TestPendingEvictionFIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := NewPending(2)
	results := make([]chan error, 3)
	conns := make([]*testConn, 3)
	for i := range 3 {
		conns[i] = newTestConn(t, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		results[i] = make(chan error, 1)
		attempt := pending.Admit(ctx, conns[i])
		require.NotNil(t, attempt)
		go func() { results[i] <- attempt.Run(0) }()
	}
	require.ErrorIs(t, readResult(t, results[0]), ErrTooManyPending)
	select {
	case <-conns[1].closed:
		t.Fatal("newer connection evicted")
	default:
	}
	cancel()
	for _, result := range results[1:] {
		require.ErrorIs(t, readResult(t, result), context.Canceled)
	}
}

func TestAdmitCanceledWhileWaiting(t *testing.T) {
	pending := NewPending(1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	firstConn := newTestConn(t, nil)
	firstConn.handshake = func(context.Context) error {
		<-firstConn.closed
		<-release
		return io.ErrClosedPipe
	}
	first := pending.Admit(context.Background(), firstConn)
	result := make(chan error, 1)
	go func() { result <- first.Run(0) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := newTestConn(t, func(context.Context) error { return nil })
	admitted := make(chan *Attempt, 1)
	go func() { admitted <- pending.Admit(ctx, second) }()
	readResult(t, firstConn.closed)
	cancel()
	require.Nil(t, readResult(t, admitted))
	releaseOnce.Do(func() { close(release) })
	require.ErrorIs(t, readResult(t, result), ErrTooManyPending)
}

type blockingCloseConn struct {
	*testConn
	closeStarted chan struct{}
	releaseClose <-chan struct{}
	once         sync.Once
}

func (c *blockingCloseConn) Close() error {
	c.once.Do(func() {
		close(c.closeStarted)
		<-c.releaseClose
	})
	return c.testConn.Close()
}

func TestAdmitWakesWhenAnotherHandshakeReleasesSlot(t *testing.T) {
	pending := NewPending(2)
	releaseOldest := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseOldest) })
	oldestConn := &blockingCloseConn{
		testConn:     newTestConn(t, nil),
		closeStarted: make(chan struct{}),
		releaseClose: releaseOldest,
	}
	oldestConn.handshake = func(context.Context) error {
		<-releaseOldest
		return context.Canceled
	}
	oldest := pending.Admit(context.Background(), oldestConn)
	oldestResult := make(chan error, 1)
	go func() { oldestResult <- oldest.Run(0) }()

	releaseOther := make(chan struct{})
	var otherOnce sync.Once
	defer otherOnce.Do(func() { close(releaseOther) })
	otherConn := newTestConn(t, func(context.Context) error {
		<-releaseOther
		return nil
	})
	other := pending.Admit(context.Background(), otherConn)
	otherResult := make(chan error, 1)
	go func() { otherResult <- other.Run(0) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replacement := newTestConn(t, func(context.Context) error { return nil })
	admitted := make(chan *Attempt, 1)
	go func() { admitted <- pending.Admit(ctx, replacement) }()
	readResult(t, oldestConn.closeStarted)
	otherOnce.Do(func() { close(releaseOther) })
	require.NoError(t, readResult(t, otherResult))
	next := readResult(t, admitted)
	require.NotNil(t, next)
	pending.mu.Lock()
	count, first := pending.count, pending.first
	pending.mu.Unlock()
	require.Equal(t, uint64(2), count)
	require.Same(t, oldest, first)
	require.NoError(t, next.Run(0))
	releaseOnce.Do(func() { close(releaseOldest) })
	require.ErrorIs(t, readResult(t, oldestResult), ErrTooManyPending)
}

func TestAdmitCanceledWhileCloseBlocked(t *testing.T) {
	pending := NewPending(1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	conn := &blockingCloseConn{
		testConn:     newTestConn(t, nil),
		closeStarted: make(chan struct{}),
		releaseClose: release,
	}
	conn.handshake = func(context.Context) error {
		<-release
		return context.Canceled
	}
	oldest := pending.Admit(context.Background(), conn)
	result := make(chan error, 1)
	go func() { result <- oldest.Run(0) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replacement := newTestConn(t, func(context.Context) error { return nil })
	admitted := make(chan *Attempt, 1)
	go func() { admitted <- pending.Admit(ctx, replacement) }()
	readResult(t, conn.closeStarted)
	cancel()
	require.Nil(t, readResult(t, admitted))
	releaseOnce.Do(func() { close(release) })
	require.ErrorIs(t, readResult(t, result), ErrTooManyPending)
}

func TestUnlimitedPending(t *testing.T) {
	const count = 32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := NewPending(0)
	results := make(chan error, count)
	conns := make([]*testConn, 0, count)
	for range count {
		conn := newTestConn(t, func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})
		conns = append(conns, conn)
		attempt := pending.Admit(ctx, conn)
		require.NotNil(t, attempt)
		go func() { results <- attempt.Run(0) }()
	}
	for _, conn := range conns {
		select {
		case <-conn.closed:
			t.Fatal("unlimited tracker evicted a handshake")
		default:
		}
	}
	cancel()
	for range count {
		require.ErrorIs(t, readResult(t, results), context.Canceled)
	}
}

func TestConcurrentAdmit(t *testing.T) {
	const count = 32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pending := NewPending(4)
	var started atomic.Int32
	results := make(chan error, count)
	for range count {
		conn := newTestConn(t, func(ctx context.Context) error {
			started.Add(1)
			<-ctx.Done()
			return ctx.Err()
		})
		go func() {
			attempt := pending.Admit(ctx, conn)
			if attempt == nil {
				results <- ctx.Err()
				return
			}
			results <- attempt.Run(0)
		}()
	}
	require.Eventually(t, func() bool { return started.Load() == count }, 5*time.Second, time.Millisecond)
	cancel()
	for range count {
		err := readResult(t, results)
		require.True(t, errors.Is(err, ErrTooManyPending) || errors.Is(err, context.Canceled), "%v", err)
	}
}

func TestHandshakeCompletionDuringEviction(t *testing.T) {
	for _, handshakeErr := range []error{nil, errors.New("underlying failure")} {
		name := "success"
		if handshakeErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			pending := NewPending(1)
			conn := newTestConn(t, nil)
			conn.handshake = func(context.Context) error {
				<-conn.closed
				return handshakeErr
			}
			first := pending.Admit(context.Background(), conn)
			result := make(chan error, 1)
			go func() { result <- first.Run(0) }()
			replacement := newTestConn(t, func(context.Context) error { return nil })
			next := pending.Admit(context.Background(), replacement)
			err := readResult(t, result)
			require.ErrorIs(t, err, ErrTooManyPending)
			if handshakeErr != nil {
				require.ErrorContains(t, err, handshakeErr.Error())
			}
			require.NoError(t, next.Run(0))
		})
	}
}
