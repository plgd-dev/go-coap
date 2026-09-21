package qblock

import (
	"bytes"
	"errors"
	"sort"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	coapMath "github.com/plgd-dev/go-coap/v3/pkg/math"
)

// ManagerConfig bounds all active normalized Q-Block operations for one peer
// connection. Packet storage and socket-level budgets belong to the adapter.
type ManagerConfig struct {
	Transfer         TransferConfig
	MaxTransfers     uint32
	MaxTokens        uint32
	MaxRetainedBytes uint64
}

var (
	ErrUnknownTransfer   = errors.New("unknown q-block transfer")
	ErrTokenInUse        = errors.New("q-block token already in use")
	ErrOperationInUse    = errors.New("q-block operation already in use")
	ErrOperationNotFound = errors.New("q-block operation not found")
	ErrLimitExceeded     = errors.New("q-block manager limit exceeded")
	ErrInvalidControl    = errors.New("invalid q-block control")
)

// DefaultManagerConfig returns conservative per-connection limits.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		Transfer:         DefaultTransferConfig(),
		MaxTransfers:     64,
		MaxTokens:        512,
		MaxRetainedBytes: 16 << 20,
	}
}

// Validate checks manager-wide limits independently from active state.
func (c ManagerConfig) Validate() error {
	if err := c.Transfer.Validate(); err != nil {
		return err
	}
	if c.MaxTransfers == 0 || c.MaxTokens == 0 || c.MaxTokens < c.MaxTransfers {
		return errors.New("invalid transfer or token limit")
	}
	if c.MaxRetainedBytes < uint64(c.Transfer.MaxBodySize) {
		return errors.New("retained byte limit is smaller than a body")
	}
	return nil
}

// Manager owns operation, token, and retained-byte registries. Its caller
// serializes access; it deliberately contains no locks or I/O.
type Manager struct {
	cfg         ManagerConfig
	nextID      TransferID
	retained    uint64
	byID        map[TransferID]*managedTransfer
	byOperation map[OperationKey]TransferID
	byToken     map[string]TransferID
}

// TransferID is stable for an active operation on this manager.
type TransferID uint64

// Output identifies the transfer responsible for an owned state-machine action.
type Output struct {
	TransferID TransferID
	Operation  OperationKey
	Action     Action
}

// Fragment is a validated inbound Q-Block body fragment.
type Fragment struct {
	Operation OperationKey
	Token     message.Token
	Kind      Kind
	Metadata  Metadata
	Block     Block
	Payload   []byte
}

// Control is a validated response/control event for a bound sender token.
type Control struct {
	Token    message.Token
	Continue *uint32
	Missing  []uint32
	Finish   error
}

type managedTransfer struct {
	operation OperationKey
	kind      Kind
	sender    *Sender
	receiver  *Receiver
	reserved  uint32
	tokens    map[string]struct{}
}

// NewManager validates configuration before allocating any operation state.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Manager{
		cfg:         cfg,
		byID:        make(map[TransferID]*managedTransfer),
		byOperation: make(map[OperationKey]TransferID),
		byToken:     make(map[string]TransferID),
	}, nil
}

// StartSender registers one sender and its first packet token atomically.
func (m *Manager) StartSender(operation OperationKey, token message.Token, kind Kind, meta Metadata, payload []byte, now time.Time, jitter float64) ([]Output, error) {
	if operation == "" {
		return nil, ErrOperationNotFound
	}
	if len(token) == 0 {
		return nil, ErrUnknownTransfer
	}
	if _, ok := m.byOperation[operation]; ok {
		return nil, ErrOperationInUse
	}
	if _, ok := m.byToken[string(token)]; ok {
		return nil, ErrTokenInUse
	}
	if uint64(len(m.byID)) >= uint64(m.cfg.MaxTransfers) || uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) || uint64(meta.Size) > m.cfg.MaxRetainedBytes-m.retained {
		return nil, ErrLimitExceeded
	}
	sender, err := NewSender(kind, m.cfg.Transfer, meta, payload, now, jitter)
	if err != nil {
		return nil, err
	}
	actions, err := sender.Start(now)
	if err != nil {
		return nil, err
	}
	m.nextID++
	id := m.nextID
	record := &managedTransfer{operation: operation, kind: kind, sender: sender, reserved: meta.Size, tokens: map[string]struct{}{string(bytes.Clone(token)): {}}}
	m.byID[id] = record
	m.byOperation[operation] = id
	m.byToken[string(token)] = id
	m.retained += uint64(meta.Size)
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

// StartReceiver registers an inbound transfer only after its first fragment is
// accepted. Every registry update is deferred until validation succeeds.
func (m *Manager) StartReceiver(fragment Fragment, now time.Time) ([]Output, error) {
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
	if uint64(len(m.byID)) >= uint64(m.cfg.MaxTransfers) || uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) || uint64(fragment.Metadata.Size) > m.cfg.MaxRetainedBytes-m.retained {
		return nil, ErrLimitExceeded
	}
	receiver, err := NewReceiver(fragment.Kind, m.cfg.Transfer, fragment.Metadata, now)
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
	record := &managedTransfer{
		operation: fragment.Operation,
		kind:      fragment.Kind,
		receiver:  receiver,
		reserved:  fragment.Metadata.Size,
		tokens:    map[string]struct{}{token: {}},
	}
	m.byID[id] = record
	m.byOperation[fragment.Operation] = id
	m.byToken[token] = id
	m.retained += uint64(fragment.Metadata.Size)
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

// TransferID returns the active transfer associated with operation. It lets an
// adapter retain an incomplete receiver that did not emit an initial action.
func (m *Manager) TransferID(operation OperationKey) (TransferID, bool) {
	id, ok := m.byOperation[operation]
	return id, ok
}

// Receive routes one subsequent inbound fragment to its established receiver.
func (m *Manager) Receive(fragment Fragment, now time.Time) ([]Output, error) {
	if len(fragment.Token) == 0 {
		return nil, ErrUnknownTransfer
	}
	id, ok := m.byToken[string(fragment.Token)]
	if !ok {
		return nil, ErrUnknownTransfer
	}
	record, ok := m.byID[id]
	if !ok || record.receiver == nil || record.operation != fragment.Operation || record.kind != fragment.Kind {
		return nil, ErrUnknownTransfer
	}
	actions, err := record.receiver.Receive(fragment.Metadata, fragment.Block, fragment.Payload, now)
	if err != nil {
		return nil, err
	}
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

// Tick advances transfers due at the caller-supplied event time in stable ID
// order, preserving each transfer machine's action order.
func (m *Manager) Tick(now time.Time) []Output {
	var outputs []Output
	ids := make([]TransferID, 0, len(m.byID))
	for id := range m.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		record, ok := m.byID[id]
		if !ok {
			continue
		}
		deadline, ok := record.nextDeadline()
		if !ok || now.Before(deadline) {
			continue
		}
		var actions []Action
		if record.sender != nil {
			actions = record.sender.Tick(now)
		} else {
			actions = record.receiver.Tick(now)
		}
		recordOutputs := m.outputs(id, record, actions)
		outputs = append(outputs, recordOutputs...)
		m.removeReleased(id, recordOutputs)
	}
	return outputs
}

// NextDeadline returns the earliest timer among active transfers.
func (m *Manager) NextDeadline() (time.Time, bool) {
	var next time.Time
	for _, record := range m.byID {
		deadline, ok := record.nextDeadline()
		if !ok || (!next.IsZero() && !deadline.Before(next)) {
			continue
		}
		next = deadline
	}
	return next, !next.IsZero()
}

// BindToken associates another packet token with an active transfer.
func (m *Manager) BindToken(id TransferID, token message.Token) error {
	record, ok := m.byID[id]
	if !ok {
		return ErrUnknownTransfer
	}
	if len(token) == 0 {
		return ErrUnknownTransfer
	}
	key := string(token)
	if _, ok := m.byToken[key]; ok {
		return ErrTokenInUse
	}
	if uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) {
		return ErrLimitExceeded
	}
	m.byToken[key] = id
	record.tokens[key] = struct{}{}
	return nil
}

// Control routes one terminal, Continue, or missing-block signal by token.
func (m *Manager) Control(control Control, now time.Time) ([]Output, error) {
	if len(control.Token) == 0 || !validControl(control) {
		return nil, ErrInvalidControl
	}
	id, ok := m.byToken[string(control.Token)]
	if !ok {
		return nil, ErrUnknownTransfer
	}
	record, ok := m.byID[id]
	if !ok || record.sender == nil {
		return nil, ErrUnknownTransfer
	}
	actions, err := controlSender(record.sender, control, now)
	if err != nil {
		return nil, err
	}
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

// ControlWithToken resolves a caller-validated transfer ID and atomically
// applies a control with its packet token. A rejected control leaves both the
// sender and token registry unchanged.
func (m *Manager) ControlWithToken(id TransferID, control Control, now time.Time) ([]Output, error) {
	if len(control.Token) == 0 || !validControl(control) {
		return nil, ErrInvalidControl
	}
	record, ok := m.byID[id]
	if !ok || record.sender == nil {
		return nil, ErrUnknownTransfer
	}

	token := string(control.Token)
	owner, bound := m.byToken[token]
	if bound && owner != id {
		return nil, ErrTokenInUse
	}

	trial := *record.sender
	trial.repairs = append([]uint32(nil), record.sender.repairs...)
	actions, err := controlSender(&trial, control, now)
	if err != nil {
		return nil, err
	}

	released := false
	for _, action := range actions {
		if action.Kind == Release {
			released = true
			break
		}
	}
	if !bound && !released && uint64(len(m.byToken)) >= uint64(m.cfg.MaxTokens) {
		return nil, ErrLimitExceeded
	}
	// A no-op Continue does not establish a new token route. Terminal paths
	// release the sender instead of retaining a just-arrived token.
	bind := !bound && !released && (len(actions) > 0 || len(control.Missing) > 0)
	record.sender = &trial
	if bind {
		m.byToken[token] = id
		record.tokens[token] = struct{}{}
	}
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs, nil
}

func controlSender(sender *Sender, control Control, now time.Time) ([]Action, error) {
	switch {
	case control.Continue != nil:
		return sender.Continue(*control.Continue, now)
	case len(control.Missing) > 0:
		return sender.Repair(control.Missing, now)
	default:
		return sender.Finish(control.Finish), nil
	}
}

// Cancel releases one operation after emitting its terminal actions.
func (m *Manager) Cancel(id TransferID, err error) []Output {
	record, ok := m.byID[id]
	if !ok {
		return nil
	}
	var actions []Action
	if record.sender != nil {
		actions = record.sender.Cancel(err)
	} else {
		actions = record.receiver.Cancel(err)
	}
	outputs := m.outputs(id, record, actions)
	m.removeReleased(id, outputs)
	return outputs
}

// Active returns the number of retained operations.
func (m *Manager) Active() uint32 {
	// ManagerConfig bounds active records to MaxTransfers, a uint32.
	return coapMath.CastTo[uint32](len(m.byID))
}

func validControl(control Control) bool {
	if control.Continue != nil {
		return len(control.Missing) == 0 && control.Finish == nil
	}
	if len(control.Missing) > 0 {
		if control.Finish != nil {
			return false
		}
		for i, number := range control.Missing {
			if i > 0 && number <= control.Missing[i-1] {
				return false
			}
		}
		return true
	}
	// Finish is an error value, so nil represents a successful finish. With no
	// Continue or Missing branch selected, this is the terminal control branch.
	return true
}

func (m *Manager) outputs(id TransferID, record *managedTransfer, actions []Action) []Output {
	outputs := make([]Output, len(actions))
	for i, action := range actions {
		action.Payload = bytes.Clone(action.Payload)
		action.Numbers = append([]uint32(nil), action.Numbers...)
		outputs[i] = Output{TransferID: id, Operation: record.operation, Action: action}
	}
	return outputs
}

func (m *Manager) removeReleased(id TransferID, outputs []Output) {
	for _, output := range outputs {
		if output.Action.Kind != Release {
			continue
		}
		record, ok := m.byID[id]
		if !ok {
			return
		}
		delete(m.byID, id)
		delete(m.byOperation, record.operation)
		for token := range record.tokens {
			delete(m.byToken, token)
		}
		m.retained -= uint64(record.reserved)
		return
	}
}

func (record *managedTransfer) nextDeadline() (time.Time, bool) {
	if record.sender != nil {
		return record.sender.NextDeadline()
	}
	return record.receiver.NextDeadline()
}
