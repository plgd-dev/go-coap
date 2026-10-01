package qblock

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestQBlockClientConfigValidation(t *testing.T) {
	c := DefaultClientConfig()
	require.NoError(t, c.Validate())
	require.Error(t, (ClientConfig{}).Validate())
	for _, n := range []uint32{0, 65537} {
		v := c
		v.MaxProbeWaiters = n
		require.Error(t, v.Validate())
		v = c
		v.MaxMIDEntries = n
		require.Error(t, v.Validate())
	}
	c.Mode = Mode(2)
	require.Error(t, c.Validate())
}
