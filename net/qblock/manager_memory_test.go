package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestManagerReceiverReservesSparseAndAssembly(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "deferred"}[deferred], func(t *testing.T) {
			cfg := DefaultManagerConfig()
			cfg.Transfer.MaxBodySize = 32
			cfg.MaxRetainedBytes, _ = bodyStorageBytes(Metadata{Size: 32, SZX: blockwise.SZX16})
			m, err := NewManager(cfg)
			require.NoError(t, err)
			now := time.Unix(100, 0)
			fragment := Fragment{Operation: "one", Token: message.Token{1}, Kind: Q1, Metadata: Metadata{Size: 32, SZX: blockwise.SZX16}, Block: Block{More: true, SZX: blockwise.SZX16}, Payload: bytes.Repeat([]byte{'x'}, 16)}
			start := m.StartReceiver
			if deferred {
				start = m.StartReceiverDeferred
			}
			_, err = start(fragment, now)
			require.NoError(t, err)
			fragment.Operation, fragment.Token = "two", message.Token{2}
			_, err = start(fragment, now)
			require.ErrorIs(t, err, ErrLimitExceeded)
			require.Equal(t, uint32(1), m.Active())
			fragment.Operation, fragment.Token, fragment.Block = "one", message.Token{1}, Block{Number: 1, SZX: blockwise.SZX16}
			outputs, err := m.Receive(fragment, now)
			require.NoError(t, err)
			require.Len(t, outputs, 1)
			require.Equal(t, bytes.Repeat([]byte{'x'}, 32), outputs[0].Action.Payload)
			id, ok := m.TransferID("one")
			require.True(t, ok)
			m.Cancel(id, nil)
			fragment.Operation, fragment.Token, fragment.Block = "two", message.Token{2}, Block{More: true, SZX: blockwise.SZX16}
			_, err = start(fragment, now)
			require.NoError(t, err, "cancellation returns the whole receiver reservation")
		})
	}
}

func TestManagerReceiverRejectsBodyWithoutAssemblyCapacity(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxBodySize = 16
	cfg.MaxRetainedBytes, _ = bodyStorageBytes(Metadata{Size: 16, SZX: blockwise.SZX16})
	cfg.MaxRetainedBytes--
	m, err := NewManager(cfg)
	require.NoError(t, err)
	_, err = m.StartReceiver(Fragment{Operation: "one", Token: message.Token{1}, Kind: Q2, Metadata: Metadata{Size: 16, SZX: blockwise.SZX16}, Block: Block{SZX: blockwise.SZX16}, Payload: bytes.Repeat([]byte{'x'}, 16)}, time.Unix(100, 0))
	require.ErrorIs(t, err, ErrLimitExceeded)
	require.Zero(t, m.Active())
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)
}

func TestManagerReceiverDeliveryReturnsReservation(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxBodySize = 16
	cfg.MaxRetainedBytes, _ = bodyStorageBytes(Metadata{Size: 16, SZX: blockwise.SZX16})
	m, err := NewManager(cfg)
	require.NoError(t, err)
	now := time.Unix(100, 0)
	fragment := Fragment{Operation: "one", Token: message.Token{1}, Kind: Q2, Metadata: Metadata{Size: 16, SZX: blockwise.SZX16}, Block: Block{SZX: blockwise.SZX16}, Payload: bytes.Repeat([]byte{'x'}, 16)}
	outputs, err := m.StartReceiver(fragment, now)
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	require.Equal(t, Deliver, outputs[0].Action.Kind)
	require.Equal(t, Release, outputs[1].Action.Kind)
	require.Zero(t, m.retained)
	_, err = m.StartSender("sender", message.Token{2}, Q1, fragment.Metadata, fragment.Payload, now, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(16), m.retained, "sender reserves one retained body")
}

func TestManagerReceiverRejectsUnbudgetedSparseIndex(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxBodySize = 1024
	cfg.MaxRetainedBytes = 2048
	m, err := NewManager(cfg)
	require.NoError(t, err)
	_, err = m.StartReceiver(Fragment{Operation: "indexed", Token: message.Token{1}, Kind: Q1, Metadata: Metadata{Size: 1024, SZX: blockwise.SZX16}, Block: Block{More: true, SZX: blockwise.SZX16}, Payload: bytes.Repeat([]byte{'x'}, 16)}, time.Unix(100, 0))
	require.ErrorIs(t, err, ErrLimitExceeded, "payload and assembly budget excludes directory/page bookkeeping")
	require.Zero(t, m.Active())
	require.Zero(t, m.retained)
}

func TestManagerReceiverExactIdentityReservation(t *testing.T) {
	meta := Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("etag-a")}
	cost, err := bodyStorageBytes(meta)
	require.NoError(t, err)
	for _, limit := range []uint64{cost - 1, cost} {
		cfg := DefaultManagerConfig()
		cfg.Transfer.MaxBodySize = 16
		cfg.MaxRetainedBytes = limit
		manager, err := NewManager(cfg)
		require.NoError(t, err)
		outputs, err := manager.StartReceiver(Fragment{Operation: "exact", Token: message.Token{1}, Kind: Q1, Metadata: meta, Block: Block{SZX: meta.SZX}, Payload: bytes.Repeat([]byte{'x'}, 16)}, time.Unix(100, 0))
		if limit < cost {
			require.ErrorIs(t, err, ErrLimitExceeded)
			require.Zero(t, manager.retained)
			continue
		}
		require.NoError(t, err)
		require.NotEmpty(t, outputs)
		require.Equal(t, cost, manager.retained)
		require.Equal(t, 6, cap(manager.byID[1].receiver.body.meta.Identity))
		manager.Cancel(1, nil)
		require.Zero(t, manager.retained)
	}
}
