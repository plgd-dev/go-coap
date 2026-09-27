package qblock

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func preparedSenderInput() (ManagerConfig, Metadata, []byte, time.Time, OperationKey) {
	cfg := DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	payload := bytes.Repeat([]byte{7}, 48)
	meta := Metadata{Size: uint32(len(payload)), SZX: blockwise.SZX16, Identity: []byte("body")}
	return cfg, meta, payload, time.Unix(100, 0), OperationKey("prepared")
}

func TestManagerPreparedSenderReservesWithoutSending(t *testing.T) {
	cfg, meta, payload, now, operation := preparedSenderInput()
	m, err := NewManager(cfg)
	require.NoError(t, err)
	id, err := m.PrepareSender(operation, message.Token{1}, Q1, meta, payload, now, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(1), m.Active())
	require.Equal(t, uint64(48), m.retained)
	require.False(t, m.byID[id].sender.started)
	deadline, ok := m.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(cfg.Transfer.Lifetime), deadline)
	require.Empty(t, m.Tick(now.Add(time.Second)))

	outputs, err := m.ActivateSender(id, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1}, outputNumbers(outputs))
	deadline, ok = m.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(time.Second+cfg.Transfer.NonTimeout), deadline)
	require.Equal(t, now.Add(cfg.Transfer.Lifetime), m.byID[id].sender.expires)
}

func TestManagerPreparedSenderRollback(t *testing.T) {
	cfg, meta, payload, now, operation := preparedSenderInput()
	m, err := NewManager(cfg)
	require.NoError(t, err)
	_, err = m.PrepareSender(operation, message.Token{1}, Q1, meta, payload, now, 0)
	require.NoError(t, err)

	cases := []struct {
		name      string
		operation OperationKey
		token     message.Token
		meta      Metadata
		jitter    float64
	}{
		{name: "empty operation", token: message.Token{2}, meta: meta},
		{name: "duplicate operation", operation: operation, token: message.Token{2}, meta: meta},
		{name: "colliding token", operation: "other", token: message.Token{1}, meta: meta},
		{name: "invalid metadata", operation: "other", token: message.Token{2}, meta: Metadata{Size: 47, SZX: blockwise.SZX16}},
		{name: "invalid jitter", operation: "other", token: message.Token{2}, meta: meta, jitter: math.NaN()},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := m.PrepareSender(tt.operation, tt.token, Q1, tt.meta, payload, now, tt.jitter)
			require.Error(t, err)
			require.Len(t, m.byID, 1)
			require.Len(t, m.byOperation, 1)
			require.Len(t, m.byToken, 1)
			require.Equal(t, uint64(48), m.retained)
		})
	}

	limitCfg := cfg
	limitCfg.Transfer.MaxBodySize = 48
	limitCfg.MaxTransfers = 1
	limitCfg.MaxTokens = 1
	limitCfg.MaxRetainedBytes = 48
	limited, err := NewManager(limitCfg)
	require.NoError(t, err)
	_, err = limited.PrepareSender(operation, message.Token{1}, Q1, meta, payload, now, 0)
	require.NoError(t, err)
	_, err = limited.PrepareSender("second", message.Token{2}, Q1, meta, payload, now, 0)
	require.ErrorIs(t, err, ErrLimitExceeded)
	require.Equal(t, uint32(1), limited.Active())
	require.Equal(t, uint64(48), limited.retained)
	require.Len(t, limited.byToken, 1)

	bytesOnlyCfg := limitCfg
	bytesOnlyCfg.MaxTransfers = 2
	bytesOnlyCfg.MaxTokens = 2
	bytesOnly, err := NewManager(bytesOnlyCfg)
	require.NoError(t, err)
	_, err = bytesOnly.PrepareSender(operation, message.Token{1}, Q1, meta, payload, now, 0)
	require.NoError(t, err)
	_, err = bytesOnly.PrepareSender("second", message.Token{2}, Q1, meta, payload, now, 0)
	require.ErrorIs(t, err, ErrLimitExceeded)
	require.Equal(t, uint32(1), bytesOnly.Active())
	require.Equal(t, uint64(48), bytesOnly.retained)
}

func TestManagerPreparedSenderExpiryAndControls(t *testing.T) {
	cfg, meta, payload, now, operation := preparedSenderInput()
	m, err := NewManager(cfg)
	require.NoError(t, err)
	token := message.Token{1}
	id, err := m.PrepareSender(operation, token, Q1, meta, payload, now, 0)
	require.NoError(t, err)
	through := uint32(1)
	_, err = m.Control(Control{Token: message.Token{1}, Continue: &through}, now)
	require.Error(t, err)
	_, err = m.ControlWithToken(id, Control{Token: message.Token{2}, Missing: []uint32{0}}, now)
	require.Error(t, err)
	require.NotContains(t, m.byToken, string(message.Token{2}))
	require.False(t, m.byID[id].sender.started)

	token[0] = 9
	payload[0] = 9
	outputs, err := m.ActivateSender(id, now)
	require.NoError(t, err)
	require.Equal(t, byte(7), outputs[0].Action.Payload[0])
	require.Contains(t, m.byToken, string(message.Token{1}))
	_, err = m.ActivateSender(id, now)
	require.ErrorIs(t, err, ErrAlreadyStarted)
	m.Cancel(id, nil)
	require.Empty(t, m.Cancel(id, nil))
	require.Zero(t, m.Active())
	require.Zero(t, m.retained)
	require.Empty(t, m.byToken)

	expired, err := NewManager(cfg)
	require.NoError(t, err)
	expiresAt := now.Add(cfg.Transfer.Lifetime)
	id, err = expired.PrepareSender("expired", message.Token{3}, Q1, meta, bytes.Repeat([]byte{7}, 48), now, 0)
	require.NoError(t, err)
	outputs, err = expired.ActivateSender(id, expiresAt)
	require.NoError(t, err)
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{outputs[0].Action.Kind, outputs[1].Action.Kind})
	require.ErrorIs(t, outputs[0].Action.Err, ErrExpired)
	require.Zero(t, expired.Active())
	require.Zero(t, expired.retained)
	require.Empty(t, expired.byToken)
}
