package qblock

import (
	"bytes"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
)

// PrepareSender registers and owns a body without transmitting its first set.
// The sender's absolute lifetime starts at preparation, including time spent
// waiting for transport admission.
func (m *Manager) PrepareSender(operation OperationKey, token message.Token, kind Kind, meta Metadata, payload []byte, now time.Time, jitter float64) (TransferID, error) {
	if operation == "" {
		return 0, ErrOperationNotFound
	}
	if len(token) == 0 && kind != Q2 {
		return 0, ErrUnknownTransfer
	}
	if _, ok := m.byOperation[operation]; ok {
		return 0, ErrOperationInUse
	}
	if _, ok := m.byToken[string(token)]; ok {
		return 0, ErrTokenInUse
	}
	if uint64(len(m.byID)) >= uint64(m.cfg.MaxTransfers) || uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) || uint64(meta.Size) > m.cfg.MaxRetainedBytes-m.retained {
		return 0, ErrLimitExceeded
	}
	sender, err := NewSender(kind, m.cfg.Transfer, meta, payload, now, jitter)
	if err != nil {
		return 0, err
	}
	m.nextID++
	id := m.nextID
	ownedToken := string(bytes.Clone(token))
	record := &managedTransfer{operation: operation, kind: kind, sender: sender, reserved: uint64(meta.Size), tokens: map[string]struct{}{ownedToken: {}}}
	m.byID[id] = record
	m.byOperation[operation] = id
	m.byToken[ownedToken] = id
	m.retained += uint64(meta.Size)
	return id, nil
}

// ActivateSender emits the first set once, retaining the preparation lifetime.
func (m *Manager) ActivateSender(id TransferID, now time.Time) ([]Output, error) {
	record, ok := m.byID[id]
	if !ok || record.sender == nil {
		return nil, ErrUnknownTransfer
	}
	actions, err := record.sender.Start(now)
	if err != nil {
		return nil, err
	}
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}
