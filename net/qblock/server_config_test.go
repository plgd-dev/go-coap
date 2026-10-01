package qblock

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestServerConfigValidation(t *testing.T) {
	c := DefaultServerConfig()
	require.NoError(t, c.Validate())
	c.MaxPeers = 0
	require.Error(t, c.Validate())
	require.Error(t, (ServerConfig{}).Validate())
}
