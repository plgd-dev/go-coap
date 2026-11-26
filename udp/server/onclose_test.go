package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionAddOnCloseAfterShutdown(t *testing.T) {
	done, doneCancel := context.WithCancel(context.Background())
	s := &Session{doneCtx: done, doneCancel: doneCancel}
	var calls int
	s.AddOnClose(func() { calls++ })
	s.shutdown()
	require.Equal(t, 1, calls)
	select {
	case <-s.Done():
	default:
		t.Fatal("shutdown did not close Done")
	}
	s.AddOnClose(func() { calls++ })
	require.Equal(t, 2, calls)
}
