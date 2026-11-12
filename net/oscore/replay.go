package oscore

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidReplayWindow = errors.New("invalid OSCORE replay window size")
	ErrInvalidReplayState  = errors.New("invalid OSCORE replay window state")
)

// ReplayWindowState is the durable state needed to continue replay protection
// after a receiver restart.
type ReplayWindowState struct {
	HighestSequence uint64
	Bitmap          uint64
	Started         bool
	WindowSize      uint8
}

// ReplayWindowStore loads and durably saves a receiver's replay window state.
// Save must atomically persist the complete state before returning nil. Use a
// store dedicated to one Context and coordinate access if receiver processes
// can concurrently use the same security context.
type ReplayWindowStore interface {
	Load() (ReplayWindowState, error)
	Save(ReplayWindowState) error
}

// ReplayWindow rejects duplicate or stale Partial IV values. Authentication is
// performed while holding the window lock so concurrent copies cannot both be
// accepted. The window advances only after verify returns nil.
type ReplayWindow struct {
	mu      sync.Mutex
	size    uint8
	highest uint64
	bitmap  uint64
	started bool
	store   ReplayWindowStore
}

// NewReplayWindow creates a sliding replay window of 1 to 64 sequence numbers.
// A size of zero selects the RFC default of 32; larger values are rejected.
func NewReplayWindow(size uint8) (*ReplayWindow, error) {
	return newReplayWindow(size, ReplayWindowState{}, nil)
}

func newReplayWindow(size uint8, state ReplayWindowState, store ReplayWindowStore) (*ReplayWindow, error) {
	if size == 0 {
		size = 32
	}
	if size > 64 {
		return nil, fmt.Errorf("%w: %d exceeds 64", ErrInvalidReplayWindow, size)
	}
	if err := validateReplayState(size, state); err != nil {
		return nil, err
	}
	if state.Started && state.WindowSize == 0 {
		state.WindowSize = size
	}
	return &ReplayWindow{
		size:    size,
		highest: state.HighestSequence,
		bitmap:  state.Bitmap,
		started: state.Started,
		store:   store,
	}, nil
}

func validateReplayState(size uint8, state ReplayWindowState) error {
	if !state.Started {
		if state.HighestSequence != 0 || state.Bitmap != 0 || state.WindowSize != 0 {
			return fmt.Errorf("%w: unstarted state contains sequence data", ErrInvalidReplayState)
		}
		return nil
	}
	if state.HighestSequence > maxPartialIV || state.Bitmap&1 == 0 {
		return fmt.Errorf("%w: invalid highest sequence or bitmap", ErrInvalidReplayState)
	}
	if state.WindowSize != 0 && state.WindowSize != size {
		return fmt.Errorf("%w: stored window size %d differs from configured size %d", ErrInvalidReplayState, state.WindowSize, size)
	}
	if size < 64 && state.Bitmap>>size != 0 {
		return fmt.Errorf("%w: bitmap exceeds configured window size", ErrInvalidReplayState)
	}
	return nil
}

// Accept verifies a sequence number and records it only after successful
// authentication.
func (w *ReplayWindow) Accept(sequence uint64, verify func() error) error {
	if verify == nil {
		return fmt.Errorf("%w: missing authentication function", ErrInvalidContext)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var bit uint64
	switch {
	case !w.started:
	case sequence > w.highest:
	case w.highest-sequence >= uint64(w.size):
		return ErrReplay
	default:
		bit = uint64(1) << (w.highest - sequence)
		if w.bitmap&bit != 0 {
			return ErrReplay
		}
	}

	if err := verify(); err != nil {
		return err
	}

	next := ReplayWindowState{
		HighestSequence: w.highest,
		Bitmap:          w.bitmap,
		Started:         w.started,
		WindowSize:      w.size,
	}
	switch {
	case !w.started:
		next.HighestSequence, next.Bitmap, next.Started = sequence, 1, true
	case sequence > w.highest:
		shift := sequence - w.highest
		if shift >= uint64(w.size) {
			next.Bitmap = 1
		} else {
			next.Bitmap = ((w.bitmap << shift) | 1) & replayWindowMask(w.size)
		}
		next.HighestSequence = sequence
	default:
		next.Bitmap |= bit
	}
	if w.store != nil {
		if err := w.store.Save(next); err != nil {
			reconcileErr := w.reconcileStoreState()
			if reconcileErr != nil {
				return fmt.Errorf("persist OSCORE replay state: %w (reload persisted state: %w)", err, reconcileErr)
			}
			return fmt.Errorf("persist OSCORE replay state: %w", err)
		}
	}
	w.highest, w.bitmap, w.started = next.HighestSequence, next.Bitmap, next.Started
	return nil
}

func (w *ReplayWindow) reconcileStoreState() error {
	state, err := w.store.Load()
	if err != nil {
		return err
	}
	if err := validateReplayState(w.size, state); err != nil {
		return err
	}
	if state.Started && state.WindowSize == 0 {
		state.WindowSize = w.size
	}
	current := ReplayWindowState{
		HighestSequence: w.highest,
		Bitmap:          w.bitmap,
		Started:         w.started,
		WindowSize:      w.size,
	}
	if !replayStateAtLeast(current, state) {
		return ErrInvalidReplayState
	}
	w.highest, w.bitmap, w.started = state.HighestSequence, state.Bitmap, state.Started
	return nil
}

func replayStateAtLeast(current, candidate ReplayWindowState) bool {
	if !current.Started {
		return true
	}
	if !candidate.Started || candidate.HighestSequence < current.HighestSequence {
		return false
	}
	required := current.Bitmap
	if candidate.HighestSequence > current.HighestSequence {
		shift := candidate.HighestSequence - current.HighestSequence
		if shift >= uint64(current.WindowSize) {
			required = 0
		} else {
			required = (required << shift) & replayWindowMask(current.WindowSize)
		}
	}
	return candidate.Bitmap&required == required
}

func replayWindowMask(size uint8) uint64 {
	if size == 64 {
		return ^uint64(0)
	}
	return (uint64(1) << size) - 1
}
