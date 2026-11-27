package server

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckAcceptErrorClosedListener(t *testing.T) {
	for _, err := range []error{io.EOF, fmt.Errorf("listener: %w", io.EOF)} {
		s := New()
		require.False(t, s.checkAcceptError(err))
		require.ErrorIs(t, s.ctx.Err(), context.Canceled)
	}
}

func TestServerRejectsNonPositiveWriteTimeout(t *testing.T) {
	s := New()
	s.cfg.WriteTimeout = 0
	require.ErrorContains(t, s.Serve(nil), "server write timeout must be positive")
}
