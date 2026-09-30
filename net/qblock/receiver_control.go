package qblock

import (
	"bytes"
	"slices"
	"time"
)

// ControlIntent identifies one receiver-generated control and the state
// revision to acknowledge after its packet or packet batch is written.
type ControlIntent struct {
	Revision uint64
	Action   Action
}

// NewDeferredReceiver uses explicit transmission commits for outbound
// controls. It otherwise follows the ordinary Receiver validation rules.
func NewDeferredReceiver(kind Kind, cfg TransferConfig, meta Metadata, now time.Time) (*Receiver, error) {
	r, err := NewReceiver(kind, cfg, meta, now)
	if err != nil {
		return nil, err
	}
	r.deferred = true
	return r, nil
}

func (r *Receiver) queueControl(action Action) {
	r.revision++
	action.Numbers = slices.Clone(action.Numbers)
	intent := &ControlIntent{Revision: r.revision, Action: action}
	if action.Kind == SendContinue {
		r.pendingContinue = intent
	} else {
		r.pendingMissing = intent
	}
}

// PendingControls returns detached copies in wire order: Continue, then
// missing-block report. At most one of each can be pending.
func (r *Receiver) PendingControls() []ControlIntent {
	if r.closed || !r.deferred {
		return nil
	}
	var intents []ControlIntent
	for _, pending := range []*ControlIntent{r.pendingContinue, r.pendingMissing} {
		if pending == nil {
			continue
		}
		copy := *pending
		copy.Action.Numbers = slices.Clone(pending.Action.Numbers)
		intents = append(intents, copy)
	}
	return intents
}

// CommitControl applies only the current matching control. A stale or
// repeated acknowledgement leaves the receiver unchanged.
func (r *Receiver) CommitControl(revision uint64, now time.Time) []Action {
	if r.closed || !r.deferred {
		return nil
	}
	if !now.Before(r.expires) {
		return r.expire()
	}
	if r.pendingContinue != nil && r.pendingContinue.Revision == revision {
		r.continued = r.pendingContinue.Action.Through + 1
		r.pendingContinue = nil
		if r.pendingMissing == nil {
			delay := r.cfg.NonReceiveTimeout
			if r.retries > 0 {
				delay = r.cfg.retryDelay(r.retries)
			}
			r.due = now.Add(delay)
		}
		return nil
	}
	if r.pendingMissing != nil && r.pendingMissing.Revision == revision {
		r.lastMissing = slices.Clone(r.pendingMissing.Action.Numbers)
		r.pendingMissing = nil
		r.retries++
		if r.pendingContinue == nil {
			r.due = now.Add(r.cfg.retryDelay(r.retries))
		}
	}
	return nil
}

// StartReceiverDeferred registers a receiver whose controls require explicit
// CommitControl calls after the adapter has transmitted them.
func (m *Manager) StartReceiverDeferred(fragment Fragment, now time.Time) ([]Output, error) {
	return m.startReceiver(fragment, now, true)
}

func (m *Manager) startReceiver(fragment Fragment, now time.Time, deferred bool) ([]Output, error) {
	if fragment.Operation == "" {
		return nil, ErrOperationNotFound
	}
	if len(fragment.Token) == 0 {
		return nil, ErrUnknownTransfer
	}
	if _, ok := m.byOperation[fragment.Operation]; ok {
		return nil, ErrOperationInUse
	}
	if _, ok := m.byToken[string(fragment.Token)]; ok {
		return nil, ErrTokenInUse
	}
	// Reserve sparse payload and the contiguous assembly before first intake.
	// uint32 body sizes doubled in uint64 cannot overflow.
	reserved := 2 * uint64(fragment.Metadata.Size)
	if uint64(len(m.byID)) >= uint64(m.cfg.MaxTransfers) || uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) || reserved > m.cfg.MaxRetainedBytes-m.retained {
		return nil, ErrLimitExceeded
	}
	var receiver *Receiver
	var err error
	if deferred {
		receiver, err = NewDeferredReceiver(fragment.Kind, m.cfg.Transfer, fragment.Metadata, now)
	} else {
		receiver, err = NewReceiver(fragment.Kind, m.cfg.Transfer, fragment.Metadata, now)
	}
	if err != nil {
		return nil, err
	}
	actions, err := receiver.Receive(fragment.Metadata, fragment.Block, fragment.Payload, now)
	if err != nil {
		return nil, err
	}
	m.nextID++
	id := m.nextID
	token := string(bytes.Clone(fragment.Token))
	record := &managedTransfer{operation: fragment.Operation, kind: fragment.Kind, receiver: receiver, reserved: reserved, tokens: map[string]struct{}{token: {}}}
	m.byID[id] = record
	m.byOperation[fragment.Operation] = id
	m.byToken[token] = id
	m.retained += reserved
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

// PendingControls returns detached current intents for an active receiver.
func (m *Manager) PendingControls(id TransferID) []ControlIntent {
	record := m.byID[id]
	if record == nil || record.receiver == nil {
		return nil
	}
	return record.receiver.PendingControls()
}

// CommitControl acknowledges a receiver control by transfer and revision.
func (m *Manager) CommitControl(id TransferID, revision uint64, now time.Time) []Output {
	record := m.byID[id]
	if record == nil || record.receiver == nil {
		return nil
	}
	actions := record.receiver.CommitControl(revision, now)
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs
}

// ReceiverProgress changes only when a new valid fragment is accepted.
func (m *Manager) ReceiverProgress(id TransferID) (uint64, bool) {
	record := m.byID[id]
	if record == nil || record.receiver == nil {
		return 0, false
	}
	return record.receiver.progress, true
}
