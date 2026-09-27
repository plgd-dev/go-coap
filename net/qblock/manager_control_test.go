package qblock

import (
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func deferredFragment(operation OperationKey, token byte, number uint32) Fragment {
	return Fragment{
		Operation: operation, Token: message.Token{token}, Kind: Q1,
		Metadata: Metadata{Size: 48, SZX: blockwise.SZX16, Identity: []byte("body")},
		Block:    Block{Number: number, More: true, SZX: blockwise.SZX16}, Payload: make([]byte, 16),
	}
}

func TestManagerDeferredControlRollbackAndOwnership(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	m, err := NewManager(cfg)
	require.NoError(t, err)
	now := time.Unix(100, 0)
	bad := deferredFragment("bad", 1, 2)
	bad.Block.More = true
	_, err = m.StartReceiverDeferred(bad, now)
	require.Error(t, err)
	require.Zero(t, m.Active())
	require.Zero(t, m.retained)
	require.Empty(t, m.byToken)

	for _, tt := range []struct {
		operation OperationKey
		token     byte
	}{{"one", 1}, {"two", 2}} {
		_, err = m.StartReceiverDeferred(deferredFragment(tt.operation, tt.token, 0), now)
		require.NoError(t, err)
	}
	id1, ok := m.TransferID("one")
	require.True(t, ok)
	id2, ok := m.TransferID("two")
	require.True(t, ok)
	progress, ok := m.ReceiverProgress(id1)
	require.True(t, ok)
	require.Equal(t, uint64(1), progress)
	require.Empty(t, m.Tick(now.Add(4*time.Second)))
	first, second := m.PendingControls(id1), m.PendingControls(id2)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Equal(t, first[0].Revision, second[0].Revision)
	first[0].Action.Numbers[0] = 99
	require.Equal(t, []uint32{1}, m.PendingControls(id1)[0].Action.Numbers)
	m.CommitControl(id1, first[0].Revision, now.Add(5*time.Second))
	require.Empty(t, m.PendingControls(id1))
	require.Len(t, m.PendingControls(id2), 1)
	require.Empty(t, m.CommitControl(999, first[0].Revision, now))
	m.Cancel(id1, nil)
	require.Empty(t, m.CommitControl(id1, first[0].Revision, now))
	require.Zero(t, func() uint64 { value, _ := m.ReceiverProgress(id1); return value }())
}
