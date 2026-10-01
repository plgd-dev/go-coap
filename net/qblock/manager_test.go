package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestDefaultManagerConfig(t *testing.T) {
	cfg := DefaultManagerConfig()
	require.Equal(t, uint32(64), cfg.MaxTransfers)
	require.Equal(t, uint32(512), cfg.MaxTokens)
	require.Equal(t, uint64(16<<20), cfg.MaxRetainedBytes)
	require.Equal(t, DefaultTransferConfig(), cfg.Transfer)
	_, err := NewManager(cfg)
	require.NoError(t, err)
}

func TestManagerOwnsSenderTokensAndPayloads(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	token := message.Token{1}
	payload := bytes.Repeat([]byte{7}, 21*16)
	meta := Metadata{Size: uint32(len(payload)), SZX: blockwise.SZX16, Identity: []byte("body")}
	now := time.Unix(100, 0)

	outputs, err := m.StartSender(key, token, Q1, meta, payload, now, 0)
	require.NoError(t, err)
	require.Len(t, outputs, 10)
	require.Equal(t, uint32(1), m.Active())
	require.Equal(t, key, outputs[0].Operation)
	require.Equal(t, SendBlock, outputs[0].Action.Kind)
	payload[0], token[0], outputs[0].Action.Payload[0] = 9, 9, 9

	through := uint32(9)
	outputs, err = m.Control(Control{Token: message.Token{1}, Continue: &through}, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, outputNumbers(outputs))
	require.Equal(t, byte(7), outputs[0].Action.Payload[0])
	require.NoError(t, m.BindToken(1, message.Token{2}))
	require.ErrorIs(t, m.BindToken(1, message.Token{2}), ErrTokenInUse)
	require.ErrorIs(t, m.BindToken(99, message.Token{3}), ErrUnknownTransfer)
}

func TestManagerControlWithTokenRollback(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	m, err := NewManager(cfg)
	require.NoError(t, err)

	operation, err := NewOperationKey([]byte("response"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	_, err = m.StartSender(operation, message.Token{1}, Q2,
		Metadata{Size: 48, SZX: blockwise.SZX16, Identity: []byte("etag")},
		bytes.Repeat([]byte{'r'}, 48), now, 0)
	require.NoError(t, err)

	id, ok := m.TransferID(operation)
	require.True(t, ok)
	beforeTokens, beforeBytes := len(m.byToken), m.retained
	beforeNext := m.byID[id].sender.next
	_, err = m.ControlWithToken(id, Control{
		Token: message.Token{2}, Missing: []uint32{2},
	}, now)
	require.ErrorIs(t, err, ErrInvalidRepair)
	require.Equal(t, beforeTokens, len(m.byToken))
	require.Equal(t, beforeBytes, m.retained)
	require.Equal(t, beforeNext, m.byID[id].sender.next)
	require.NotContains(t, m.byToken, string(message.Token{2}))
}

func TestManagerControlWithTokenSuccess(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	m, err := NewManager(cfg)
	require.NoError(t, err)

	operation, err := NewOperationKey([]byte("response"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	_, err = m.StartSender(operation, message.Token{1}, Q2,
		Metadata{Size: 48, SZX: blockwise.SZX16, Identity: []byte("etag")},
		bytes.Repeat([]byte{'r'}, 48), now, 0)
	require.NoError(t, err)

	id, ok := m.TransferID(operation)
	require.True(t, ok)
	outputs, err := m.ControlWithToken(id, Control{
		Token: message.Token{2}, Missing: []uint32{0},
	}, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, outputNumbers(outputs))
	require.Equal(t, id, m.byToken[string(message.Token{2})])
}

func TestManagerControlWithTokenExpiresWithoutBindingFreshToken(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.MaxTransfers = 1
	cfg.MaxTokens = 1
	m, err := NewManager(cfg)
	require.NoError(t, err)

	operation, err := NewOperationKey([]byte("response"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	_, err = m.StartSender(operation, message.Token{1}, Q2,
		Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("etag")},
		bytes.Repeat([]byte{'r'}, 16), now, 0)
	require.NoError(t, err)

	id, ok := m.TransferID(operation)
	require.True(t, ok)
	outputs, err := m.ControlWithToken(id, Control{
		Token: message.Token{2}, Missing: []uint32{0},
	}, now.Add(cfg.Transfer.Lifetime))
	require.NoError(t, err)
	require.Contains(t, outputs, Output{TransferID: id, Operation: operation, Action: Action{Kind: Release}})
	require.Zero(t, m.Active())
	require.NotContains(t, m.byToken, string(message.Token{2}))
}

func TestManagerControlWithTokenRejectsInvalidRoutesWithoutMutation(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	now := time.Unix(100, 0)
	meta := Metadata{Size: 48, SZX: blockwise.SZX16, Identity: []byte("etag")}
	first, err := NewOperationKey([]byte("first"))
	require.NoError(t, err)
	second, err := NewOperationKey([]byte("second"))
	require.NoError(t, err)
	_, err = m.StartSender(first, message.Token{1}, Q2, meta, bytes.Repeat([]byte{'a'}, 48), now, 0)
	require.NoError(t, err)
	_, err = m.StartSender(second, message.Token{2}, Q2, meta, bytes.Repeat([]byte{'b'}, 48), now, 0)
	require.NoError(t, err)
	id, ok := m.TransferID(first)
	require.True(t, ok)

	assertUnchanged := func(t *testing.T, token message.Token, control Control, want error) {
		t.Helper()
		beforeTokens, beforeBytes := len(m.byToken), m.retained
		beforeNext := m.byID[id].sender.next
		_, err := m.ControlWithToken(id, control, now)
		require.ErrorIs(t, err, want)
		require.Equal(t, beforeTokens, len(m.byToken))
		require.Equal(t, beforeBytes, m.retained)
		require.Equal(t, beforeNext, m.byID[id].sender.next)
		require.NotContains(t, m.byToken, string(token))
	}

	t.Run("wrong transfer", func(t *testing.T) {
		_, err := m.ControlWithToken(99, Control{Token: message.Token{3}, Missing: []uint32{0}}, now)
		require.ErrorIs(t, err, ErrUnknownTransfer)
	})
	t.Run("cross transfer token", func(t *testing.T) {
		beforeTokens, beforeBytes := len(m.byToken), m.retained
		_, err := m.ControlWithToken(id, Control{Token: message.Token{2}, Missing: []uint32{0}}, now)
		require.ErrorIs(t, err, ErrTokenInUse)
		require.Equal(t, beforeTokens, len(m.byToken))
		require.Equal(t, beforeBytes, m.retained)
		secondID, ok := m.TransferID(second)
		require.True(t, ok)
		require.Equal(t, secondID, m.byToken[string(message.Token{2})])
	})
	t.Run("malformed control", func(t *testing.T) {
		through := uint32(1)
		assertUnchanged(t, message.Token{3}, Control{Token: message.Token{3}, Continue: &through, Missing: []uint32{0}}, ErrInvalidControl)
	})
	t.Run("invalid repair", func(t *testing.T) {
		assertUnchanged(t, message.Token{4}, Control{Token: message.Token{4}, Missing: []uint32{3}}, ErrInvalidRepair)
	})
}

func TestManagerControlWithTokenBindsAcceptedRepairButNotNoopContinue(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	m, err := NewManager(cfg)
	require.NoError(t, err)
	now := time.Unix(100, 0)
	operation, err := NewOperationKey([]byte("response"), []byte("tag"))
	require.NoError(t, err)
	_, err = m.StartSender(operation, message.Token{1}, Q2,
		Metadata{Size: 48, SZX: blockwise.SZX16, Identity: []byte("etag")},
		bytes.Repeat([]byte{'r'}, 48), now, 0)
	require.NoError(t, err)
	id, ok := m.TransferID(operation)
	require.True(t, ok)

	stale := uint32(0)
	outputs, err := m.ControlWithToken(id, Control{Token: message.Token{2}, Continue: &stale}, now)
	require.NoError(t, err)
	require.Empty(t, outputs)
	require.NotContains(t, m.byToken, string(message.Token{2}))

	outputs, err = m.ControlWithToken(id, Control{Token: message.Token{3}, Missing: []uint32{0}}, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, outputNumbers(outputs))
	require.Equal(t, id, m.byToken[string(message.Token{3})])
}

func TestManagerRejectsConflictsAndReleasesSender(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxBodySize = 16
	cfg.MaxRetainedBytes = 16
	m, err := NewManager(cfg)
	require.NoError(t, err)
	first, err := NewOperationKey([]byte("first"))
	require.NoError(t, err)
	second, err := NewOperationKey([]byte("second"))
	require.NoError(t, err)
	meta := Metadata{Size: 16}
	now := time.Unix(100, 0)
	_, err = m.StartSender(first, message.Token{1}, Q1, meta, make([]byte, 16), now, 0)
	require.NoError(t, err)
	_, err = m.StartSender(first, message.Token{2}, Q1, meta, make([]byte, 16), now, 0)
	require.ErrorIs(t, err, ErrOperationInUse)
	_, err = m.StartSender(second, message.Token{1}, Q1, meta, make([]byte, 16), now, 0)
	require.ErrorIs(t, err, ErrTokenInUse)
	_, err = m.StartSender(second, message.Token{2}, Q1, meta, make([]byte, 16), now, 0)
	require.ErrorIs(t, err, ErrLimitExceeded)

	outputs := m.Cancel(1, ErrCanceled)
	require.Len(t, outputs, 2)
	require.Zero(t, m.Active())
	_, err = m.StartSender(second, message.Token{1}, Q1, meta, make([]byte, 16), now, 0)
	require.NoError(t, err)
}

func outputNumbers(outputs []Output) []uint32 {
	result := make([]uint32, 0, len(outputs))
	for _, output := range outputs {
		if output.Action.Kind == SendBlock {
			result = append(result, output.Action.Block.Number)
		}
	}
	return result
}

func TestManagerConfigRejectsInvalidLimits(t *testing.T) {
	for _, update := range []func(*ManagerConfig){
		func(c *ManagerConfig) { c.MaxTransfers = 0 },
		func(c *ManagerConfig) { c.MaxTokens = 0 },
		func(c *ManagerConfig) { c.MaxTokens = c.MaxTransfers - 1 },
		func(c *ManagerConfig) { c.MaxRetainedBytes = 0 },
		func(c *ManagerConfig) { c.MaxRetainedBytes = uint64(c.Transfer.MaxBodySize - 1) },
		func(c *ManagerConfig) { c.Transfer.NonTimeout = 0 },
	} {
		cfg := DefaultManagerConfig()
		update(&cfg)
		require.Error(t, cfg.Validate())
		_, err := NewManager(cfg)
		require.Error(t, err)
	}
}

func TestManagerStartReceiverRollsBackInvalidAndConflictingFirstFragments(t *testing.T) {
	newManager := func(t *testing.T) *Manager {
		t.Helper()
		m, err := NewManager(DefaultManagerConfig())
		require.NoError(t, err)
		return m
	}
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	otherKey, err := NewOperationKey([]byte("other"), []byte("tag"))
	require.NoError(t, err)
	valid := Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("body")},
		Block:     Block{SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{7}, 16),
	}

	for name, first := range map[string]Fragment{
		"malformed payload": {
			Operation: key,
			Token:     message.Token{1},
			Kind:      Q1,
			Metadata:  Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("body")},
			Block:     Block{SZX: blockwise.SZX16},
			Payload:   []byte{7},
		},
		"malformed metadata": {
			Operation: key,
			Token:     message.Token{1},
			Kind:      Q1,
			Metadata:  Metadata{Size: 16, SZX: blockwise.SZX(7), Identity: []byte("body")},
			Block:     Block{SZX: blockwise.SZX16},
			Payload:   bytes.Repeat([]byte{7}, 16),
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newManager(t)
			outputs, err := m.StartReceiver(first, time.Unix(100, 0))
			require.Error(t, err)
			require.Empty(t, outputs)
			require.Zero(t, m.Active())
			require.Empty(t, m.byOperation)
			require.Empty(t, m.byToken)
			require.Zero(t, m.retained)
		})
	}

	for name, reserve := range map[string]func(*Manager){
		"operation": func(m *Manager) {
			_, err := m.StartReceiver(valid, time.Unix(100, 0))
			require.NoError(t, err)
		},
		"token": func(m *Manager) {
			fragment := valid
			fragment.Operation = otherKey
			_, err := m.StartReceiver(fragment, time.Unix(100, 0))
			require.NoError(t, err)
		},
	} {
		t.Run("conflicting "+name, func(t *testing.T) {
			m := newManager(t)
			reserve(m)
			beforeOperations, beforeTokens, beforeRetained := len(m.byOperation), len(m.byToken), m.retained
			outputs, err := m.StartReceiver(valid, time.Unix(100, 0))
			require.Error(t, err)
			require.Empty(t, outputs)
			require.Equal(t, uint32(1), m.Active())
			require.Len(t, m.byOperation, beforeOperations)
			require.Len(t, m.byToken, beforeTokens)
			require.Equal(t, beforeRetained, m.retained)
		})
	}
}

func TestManagerQ1ReceiverDeliversOnceAndRetainsDuplicateRecord(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	meta := Metadata{Size: 17, SZX: blockwise.SZX16, Identity: []byte("body")}

	outputs, err := m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{Number: 1, More: false, SZX: blockwise.SZX16},
		Payload:   []byte("q"),
	}, now)
	require.NoError(t, err)
	require.Empty(t, outputs)
	require.Equal(t, uint32(1), m.Active())

	require.NoError(t, m.BindToken(1, message.Token{2}))
	outputs, err = m.Receive(Fragment{
		Operation: key,
		Token:     message.Token{2},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{Number: 0, More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'p'}, 16),
	}, now)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, TransferID(1), outputs[0].TransferID)
	require.Equal(t, Deliver, outputs[0].Action.Kind)
	require.Equal(t, append(bytes.Repeat([]byte{'p'}, 16), 'q'), outputs[0].Action.Payload)
	require.Equal(t, uint32(1), m.Active())

	outputs, err = m.Receive(Fragment{
		Operation: key,
		Token:     message.Token{2},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{Number: 0, More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'p'}, 16),
	}, now)
	require.NoError(t, err)
	require.Len(t, outputs, 1)
	require.Equal(t, TransferID(1), outputs[0].TransferID)
	require.Equal(t, Duplicate, outputs[0].Action.Kind)
	require.Equal(t, uint32(1), m.Active())
}

func TestManagerQ2ReceiverReleasesAfterDelivery(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("etag"))
	require.NoError(t, err)
	meta := Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("representation")}
	fragment := Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q2,
		Metadata:  meta,
		Block:     Block{SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}

	outputs, err := m.StartReceiver(fragment, time.Unix(100, 0))
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	require.Equal(t, Deliver, outputs[0].Action.Kind)
	require.Equal(t, bytes.Repeat([]byte{'q'}, 16), outputs[0].Action.Payload)
	require.Equal(t, Release, outputs[1].Action.Kind)
	require.Zero(t, m.Active())
	require.Empty(t, m.byOperation)
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)

	outputs, err = m.Receive(fragment, time.Unix(100, 0))
	require.ErrorIs(t, err, ErrUnknownTransfer)
	require.Empty(t, outputs)
	require.Zero(t, m.Active())
}

func TestManagerReceiverRetryExhaustionReleasesState(t *testing.T) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.NonMaxRetransmit = 0
	m, err := NewManager(cfg)
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	meta := Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("body")}

	outputs, err := m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}, now)
	require.NoError(t, err)
	require.Empty(t, outputs)

	outputs = m.Tick(now.Add(cfg.Transfer.NonReceiveTimeout))
	require.Len(t, outputs, 2)
	require.Equal(t, Complete, outputs[0].Action.Kind)
	require.ErrorIs(t, outputs[0].Action.Err, ErrRetriesExhausted)
	require.Equal(t, Release, outputs[1].Action.Kind)
	require.Zero(t, m.Active())
	require.Empty(t, m.byOperation)
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)
}

func TestManagerCancelReleasesReceiverState(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	meta := Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("body")}
	_, err = m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}, time.Unix(100, 0))
	require.NoError(t, err)

	outputs := m.Cancel(1, ErrCanceled)
	require.Len(t, outputs, 2)
	require.Equal(t, Complete, outputs[0].Action.Kind)
	require.ErrorIs(t, outputs[0].Action.Err, ErrCanceled)
	require.Equal(t, Release, outputs[1].Action.Kind)
	require.Zero(t, m.Active())
	require.Empty(t, m.byOperation)
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)
}

func TestManagerStartReceiverLimitFailuresLeaveStateUnchanged(t *testing.T) {
	newKey := func(t *testing.T, value string) OperationKey {
		t.Helper()
		key, err := NewOperationKey([]byte(value))
		require.NoError(t, err)
		return key
	}
	fragment := func(key OperationKey, token byte) Fragment {
		return Fragment{
			Operation: key,
			Token:     message.Token{token},
			Kind:      Q1,
			Metadata:  Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("body")},
			Block:     Block{SZX: blockwise.SZX16},
			Payload:   bytes.Repeat([]byte{'q'}, 16),
		}
	}
	for name, configure := range map[string]func(*ManagerConfig){
		"transfer": func(cfg *ManagerConfig) {
			cfg.MaxTransfers = 1
			cfg.MaxTokens = 2
		},
		"token": func(cfg *ManagerConfig) {
			cfg.MaxTransfers = 2
			cfg.MaxTokens = 2
		},
		"retained bytes": func(cfg *ManagerConfig) {
			cfg.Transfer.MaxBodySize = 16
			cfg.MaxRetainedBytes, _ = bodyStorageBytes(fragment(newKey(t, "first"), 1).Metadata)
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultManagerConfig()
			configure(&cfg)
			m, err := NewManager(cfg)
			require.NoError(t, err)
			_, err = m.StartReceiver(fragment(newKey(t, "first"), 1), time.Unix(100, 0))
			require.NoError(t, err)
			if name == "token" {
				require.NoError(t, m.BindToken(1, message.Token{2}))
			}
			beforeOperations, beforeTokens, beforeRetained := len(m.byOperation), len(m.byToken), m.retained

			outputs, err := m.StartReceiver(fragment(newKey(t, "second"), 3), time.Unix(100, 0))
			require.ErrorIs(t, err, ErrLimitExceeded)
			require.Empty(t, outputs)
			require.Len(t, m.byOperation, beforeOperations)
			require.Len(t, m.byToken, beforeTokens)
			require.Equal(t, beforeRetained, m.retained)
		})
	}
}

func TestManagerRejectsMismatchedReceiverFragmentsWithoutMutation(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	otherKey, err := NewOperationKey([]byte("other"), []byte("tag"))
	require.NoError(t, err)
	meta := Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("body")}
	fragment := Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}
	_, err = m.StartReceiver(fragment, time.Unix(100, 0))
	require.NoError(t, err)
	beforeOperations, beforeTokens, beforeRetained := len(m.byOperation), len(m.byToken), m.retained

	for name, update := range map[string]func(*Fragment){
		"operation": func(fragment *Fragment) { fragment.Operation = otherKey },
		"kind":      func(fragment *Fragment) { fragment.Kind = Q2 },
		"metadata":  func(fragment *Fragment) { fragment.Metadata.Identity = []byte("other body") },
	} {
		t.Run(name, func(t *testing.T) {
			incoming := fragment
			update(&incoming)
			outputs, err := m.Receive(incoming, time.Unix(100, 0))
			require.Error(t, err)
			require.Empty(t, outputs)
			require.Len(t, m.byOperation, beforeOperations)
			require.Len(t, m.byToken, beforeTokens)
			require.Equal(t, beforeRetained, m.retained)
		})
	}
}

func TestManagerReceiverOwnsInboundAndDeliveryPayloads(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	meta := Metadata{Size: 17, SZX: blockwise.SZX16, Identity: []byte("body")}
	last := []byte("z")
	_, err = m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{Number: 1, SZX: blockwise.SZX16},
		Payload:   last,
	}, time.Unix(100, 0))
	require.NoError(t, err)
	last[0] = 'x'
	require.NoError(t, m.BindToken(1, message.Token{2}))
	first := bytes.Repeat([]byte{'p'}, 16)

	outputs, err := m.Receive(Fragment{
		Operation: key,
		Token:     message.Token{2},
		Kind:      Q1,
		Metadata:  meta,
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   first,
	}, time.Unix(100, 0))
	require.NoError(t, err)
	first[0] = 'x'
	require.Len(t, outputs, 1)
	require.Equal(t, append(bytes.Repeat([]byte{'p'}, 16), 'z'), outputs[0].Action.Payload)

	outputs[0].Action.Payload[0] = 'x'
	require.Equal(t, byte('p'), m.byID[1].receiver.body.pages[0].payload[0])
}

func TestManagerRejectsControlForReceiverTokenWithoutMutation(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	_, err = m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q1,
		Metadata:  Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("body")},
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}, time.Unix(100, 0))
	require.NoError(t, err)
	through := uint32(0)

	outputs, err := m.Control(Control{Token: message.Token{1}, Continue: &through}, time.Unix(100, 0))
	require.ErrorIs(t, err, ErrUnknownTransfer)
	require.Empty(t, outputs)
	require.Equal(t, uint32(1), m.Active())
	require.Len(t, m.byToken, 1)
	cost, _ := bodyStorageBytes(Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("body")})
	require.Equal(t, cost, m.retained)
}

func TestManagerTickUsesEarliestDeadlineAndTransferIDOrder(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	now := time.Unix(100, 0)
	senderMeta := Metadata{Size: 11 * 16, SZX: blockwise.SZX16, Identity: []byte("sender")}
	startSender := func(t *testing.T, operation string, token byte, jitter float64) {
		t.Helper()
		key, err := NewOperationKey([]byte(operation))
		require.NoError(t, err)
		_, err = m.StartSender(key, message.Token{token}, Q1, senderMeta, bytes.Repeat([]byte{token}, int(senderMeta.Size)), now, jitter)
		require.NoError(t, err)
	}
	startSender(t, "first", 1, 0)
	startSender(t, "second", 2, 1)
	receiverKey, err := NewOperationKey([]byte("receiver"))
	require.NoError(t, err)
	_, err = m.StartReceiver(Fragment{
		Operation: receiverKey,
		Token:     message.Token{3},
		Kind:      Q1,
		Metadata:  Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("receiver")},
		Block:     Block{More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{3}, 16),
	}, now)
	require.NoError(t, err)
	startSender(t, "fourth", 4, 0)

	deadline, ok := m.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(2*time.Second), deadline)
	require.Empty(t, m.Tick(now.Add(time.Second)))

	outputs := m.Tick(now.Add(2 * time.Second))
	require.Equal(t, []TransferID{1, 4}, []TransferID{outputs[0].TransferID, outputs[1].TransferID})
	require.Equal(t, []uint32{10, 10}, []uint32{outputs[0].Action.Block.Number, outputs[1].Action.Block.Number})
	deadline, ok = m.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(3*time.Second), deadline)

	outputs = m.Tick(deadline)
	require.Len(t, outputs, 1)
	require.Equal(t, TransferID(2), outputs[0].TransferID)
	require.Equal(t, SendBlock, outputs[0].Action.Kind)
	require.Equal(t, uint32(10), outputs[0].Action.Block.Number)
	deadline, ok = m.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(4*time.Second), deadline)

	outputs = m.Tick(deadline)
	require.Len(t, outputs, 1)
	require.Equal(t, TransferID(3), outputs[0].TransferID)
	require.Equal(t, RequestMissing, outputs[0].Action.Kind)
	require.Equal(t, []uint32{1}, outputs[0].Action.Numbers)
}

func TestManagerReleaseAllowsOperationReuseAndRejectsStaleToken(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	meta := Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("body")}
	now := time.Unix(100, 0)
	_, err = m.StartSender(key, message.Token{1}, Q1, meta, bytes.Repeat([]byte{'q'}, 16), now, 0)
	require.NoError(t, err)
	outputs, err := m.Control(Control{Token: message.Token{1}, Finish: nil}, now)
	require.NoError(t, err)
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{outputs[0].Action.Kind, outputs[1].Action.Kind})

	_, err = m.StartSender(key, message.Token{2}, Q1, meta, bytes.Repeat([]byte{'q'}, 16), now, 0)
	require.NoError(t, err)
	through := uint32(0)
	outputs, err = m.Control(Control{Token: message.Token{1}, Continue: &through}, now)
	require.ErrorIs(t, err, ErrUnknownTransfer)
	require.Empty(t, outputs)
	require.Equal(t, uint32(1), m.Active())
}

func TestManagerTransferIDFindsActiveReceiverWithoutActions(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("etag"))
	require.NoError(t, err)
	_, err = m.StartReceiver(Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q2,
		Metadata:  Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("etag")},
		Block:     Block{Number: 0, More: true, SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'a'}, 16),
	}, time.Unix(100, 0))
	require.NoError(t, err)

	id, ok := m.TransferID(key)
	require.True(t, ok)
	require.Equal(t, TransferID(1), id)
}
