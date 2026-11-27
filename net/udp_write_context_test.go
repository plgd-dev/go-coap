package net

import (
	"context"
	"errors"
	stdnet "net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testDeadlineSetter struct {
	mu          sync.Mutex
	deadlines   []time.Time
	interrupted chan struct{}
	err         error
}

func TestUDPWriteDeadlineInterruptsBlockedWriteAndResets(t *testing.T) {
	writer, reader := stdnet.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withUDPWriteDeadline(ctx, writer.SetWriteDeadline, func() bool { return false }, func() error {
			close(started)
			_, err := writer.Write([]byte("first"))
			return err
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the write")
	}

	go func() {
		_, err := writer.Write([]byte("next"))
		done <- err
	}()
	require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))
	got := make([]byte, 4)
	_, err := reader.Read(got)
	require.NoError(t, err)
	require.Equal(t, "next", string(got))
	require.NoError(t, <-done)
}

func TestUDPWriteDeadlineTimesOutBlockedWrite(t *testing.T) {
	writer, reader := stdnet.Pipe()
	defer writer.Close()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := withUDPWriteDeadline(ctx, writer.SetWriteDeadline, func() bool { return false }, func() error {
		_, err := writer.Write([]byte("blocked"))
		return err
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
}

func TestUDPConnMulticastCallbackCanWrite(t *testing.T) {
	conn, err := NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()
	callback := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- conn.WriteMulticast(context.Background(), &stdnet.UDPAddr{IP: stdnet.IPv4(224, 0, 1, 187), Port: 5683}, []byte("multicast"),
			WithMulticastInterface(stdnet.Interface{Index: -1, Name: "nonexistent-interface"}),
			WithMulticastSource(stdnet.IPv4(127, 0, 0, 1)),
			WithMulticastInterfaceError(func(_ *stdnet.Interface, _ error) {
				callback <- conn.WriteWithContext(context.Background(), conn.LocalAddr().(*stdnet.UDPAddr), []byte("unicast"))
			}),
		)
	}()
	select {
	case err := <-callback:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("multicast callback could not write")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("multicast write did not finish")
	}
}

func (s *testDeadlineSetter) set(deadline time.Time) error {
	s.mu.Lock()
	s.deadlines = append(s.deadlines, deadline)
	s.mu.Unlock()
	if !deadline.IsZero() {
		s.interrupted <- struct{}{}
	}
	return s.err
}

func TestUDPWriteDeadlineCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setter := &testDeadlineSetter{interrupted: make(chan struct{}, 1)}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withUDPWriteDeadline(ctx, setter.set, func() bool { return false }, func() error {
			close(started)
			<-setter.interrupted
			return os.ErrDeadlineExceeded
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("canceled write did not finish")
	}
	setter.mu.Lock()
	defer setter.mu.Unlock()
	require.Len(t, setter.deadlines, 3)
	require.True(t, setter.deadlines[0].IsZero())
	require.False(t, setter.deadlines[1].IsZero())
	require.True(t, setter.deadlines[2].IsZero())
}

func TestUDPWriteDeadlineTimeoutAndErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var initial time.Time
	var cleared bool
	err := withUDPWriteDeadline(ctx, func(deadline time.Time) error {
		if deadline.IsZero() {
			cleared = true
		} else if initial.IsZero() {
			initial = deadline
		}
		return nil
	}, func() bool { return false }, func() error {
		time.Sleep(time.Until(initial))
		return os.ErrDeadlineExceeded
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, cleared)

	expected := errors.New("deadline failure")
	err = withUDPWriteDeadline(context.Background(), func(time.Time) error { return expected }, func() bool { return false }, func() error {
		t.Fatal("write called after setting deadline failed")
		return nil
	})
	require.ErrorIs(t, err, expected)
}

func TestUDPWriteGateRespectsContext(t *testing.T) {
	conn := &UDPConn{}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- conn.withWriteContext(context.Background(), func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := conn.withWriteContext(ctx, func() error {
		t.Error("queued write ran after its context expired")
		return nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, conn.withWriteContext(context.Background(), func() error { return nil }))
}
