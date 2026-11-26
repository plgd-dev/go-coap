package onclose

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallbacksDuringAndAfterClose(t *testing.T) {
	var callbacks Callbacks
	var calls []int
	callbacks.Add(func() {
		calls = append(calls, 1)
		callbacks.Add(func() { calls = append(calls, 2) })
	})
	callbacks.Add(func() { calls = append(calls, 3) })
	callbacks.Close()
	callbacks.Close()
	callbacks.Add(func() { calls = append(calls, 4) })
	require.Equal(t, []int{1, 2, 3, 4}, calls)
}

func TestCallbacksConcurrentRegistration(t *testing.T) {
	const count = 1000
	var callbacks Callbacks
	calls := make([]atomic.Int32, count)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			callbacks.Add(func() { calls[i].Add(1) })
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		callbacks.Close()
	}()
	close(start)
	wg.Wait()
	for i := range calls {
		require.Equal(t, int32(1), calls[i].Load(), "callback %d", i)
	}
}

func TestCallbacksRegisterWhileCloseCallbackRunning(t *testing.T) {
	var callbacks Callbacks
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	callbacks.Add(func() {
		close(started)
		<-release
	})
	go func() {
		callbacks.Close()
		close(done)
	}()
	<-started
	var called bool
	callbacks.Add(func() { called = true })
	require.True(t, called)
	close(release)
	<-done
}
