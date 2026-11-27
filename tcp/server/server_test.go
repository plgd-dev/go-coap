package server

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerRejectsNonPositiveWriteTimeout(t *testing.T) {
	s := New()
	s.cfg.WriteTimeout = 0
	require.ErrorContains(t, s.Serve(nil), "server write timeout must be positive")
}
