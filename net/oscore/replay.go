package oscore

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidReplayWindow = errors.New("invalid OSCORE replay window size")
	ErrInvalidReplayState  = errors.New("invalid OSCORE replay window state")
	ErrInternalProcessing  = errors.New("internal OSCORE processing failure")
)

// ReplayWindowState is the durable state needed to continue replay protection
// after a receiver restart.
type ReplayWindowState struct {
	HighestSequence uint64
	Bitmap          uint64
	Started         bool
	WindowSize      uint8
}

// ReplayWindowStore loads and durably saves a receiver's replay state. Save
// must atomically persist sufficient replay state before returning nil.
// Stores implementing ReplayWindowHighWaterStore may persist a stricter
// exclusive sequence cutoff instead of the complete bitmap. Use a store
// dedicated to one Context and coordinate access if receiver processes can
// concurrently use the same security context.
type ReplayWindowStore interface {
	Load() (ReplayWindowState, error)
	Save(ReplayWindowState) error
}

// ReplayWindowHighWaterStore optionally persists an exclusive sequence
// boundary in batches. After restart, sequences below the boundary are
// rejected, trading a bounded amount of availability for fewer durable writes.
type ReplayWindowHighWaterStore interface {
	LoadHighWater() (uint64, bool, error)
	SaveHighWater(uint64) error
}

// ReplayWindowHighWaterMarginProvider optionally configures the number of
// accepted sequence advances between durable high-water mark writes.
type ReplayWindowHighWaterMarginProvider interface {
	ReplayWindowHighWaterMargin() uint64
}

const (
	DefaultReplayHighWaterMargin = uint64(256)
	MaxReplayHighWaterMargin     = uint64(1024)
)

// ReplayWindow rejects duplicate or stale Partial IV values. Authentication is
// performed while holding the window lock so concurrent copies cannot both be
// accepted. The window advances only after verify returns nil.
type ReplayWindow struct {
	mu           sync.Mutex
	size         uint8
	highest      uint64
	bitmap       uint64
	started      bool
	store        ReplayWindowStore
	highWater    uint64
	highWaterSet bool
}

// NewReplayWindow creates a sliding replay window of 1 to 64 sequence numbers.
// A size of zero selects the RFC default of 32; larger values are rejected.
func NewReplayWindow(size uint8) (*ReplayWindow, error) {
	return newReplayWindow(size, ReplayWindowState{}, nil, false)
}

func newReplayWindow(size uint8, state ReplayWindowState, store ReplayWindowStore, highWaterSet bool) (*ReplayWindow, error) {
	if size == 0 {
		size = 32
	}
	if size > 64 {
		return nil, fmt.Errorf("%w: %d exceeds 64", ErrInvalidReplayWindow, size)
	}
	if err := validateReplayHighWaterMargin(store); err != nil {
		return nil, err
	}
	if err := validateReplayState(size, state); err != nil {
		return nil, err
	}
	if state.Started && state.WindowSize == 0 {
		state.WindowSize = size
	}
	window := &ReplayWindow{
		size:    size,
		highest: state.HighestSequence,
		bitmap:  state.Bitmap,
		started: state.Started,
		store:   store,
	}
	if highWaterSet && state.Started {
		window.highWater = state.HighestSequence + 1
		window.highWaterSet = highWaterSet
	}
	return window, nil
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
		return fmt.Errorf("%w: %w: missing authentication function", ErrInternalProcessing, ErrInvalidContext)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	bit, err := w.sequenceBit(sequence)
	if err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	next := w.nextState(sequence, bit)
	if err := w.persistState(next); err != nil {
		return err
	}
	w.highest, w.bitmap, w.started = next.HighestSequence, next.Bitmap, next.Started
	return nil
}

func (w *ReplayWindow) sequenceBit(sequence uint64) (uint64, error) {
	var bit uint64
	switch {
	case !w.started:
	case sequence > w.highest:
	case w.highest-sequence >= uint64(w.size):
		return 0, ErrReplay
	default:
		bit = uint64(1) << (w.highest - sequence)
		if w.bitmap&bit != 0 {
			return 0, ErrReplay
		}
	}
	return bit, nil
}

func (w *ReplayWindow) nextState(sequence, bit uint64) ReplayWindowState {
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
	return next
}

func (w *ReplayWindow) persistState(next ReplayWindowState) error {
	if w.store == nil {
		return nil
	}
	persistErr := w.saveState(next)
	if persistErr == nil {
		return nil
	}
	reconcileErr := w.reconcileStoreState()
	if reconcileErr != nil {
		return fmt.Errorf("%w: persist OSCORE replay state: %w (reload persisted state: %w)",
			ErrInternalProcessing, persistErr, reconcileErr)
	}
	return fmt.Errorf("%w: persist OSCORE replay state: %w", ErrInternalProcessing, persistErr)
}

func (w *ReplayWindow) saveState(next ReplayWindowState) error {
	highWaterStore, ok := w.store.(ReplayWindowHighWaterStore)
	if !ok {
		return w.store.Save(next)
	}
	if w.highWaterSet && next.HighestSequence < w.highWater {
		return nil
	}
	margin, err := replayHighWaterMargin(highWaterStore)
	if err != nil {
		return err
	}
	highWater := min(next.HighestSequence+margin, maxPartialIV+1)
	if err := highWaterStore.SaveHighWater(highWater); err != nil {
		return err
	}
	w.highWater = highWater
	w.highWaterSet = true
	return nil
}

func validateReplayHighWaterMargin(store ReplayWindowStore) error {
	highWaterStore, ok := store.(ReplayWindowHighWaterStore)
	if !ok {
		return nil
	}
	_, err := replayHighWaterMargin(highWaterStore)
	return err
}

func replayHighWaterMargin(store ReplayWindowHighWaterStore) (uint64, error) {
	margin := DefaultReplayHighWaterMargin
	if provider, ok := store.(ReplayWindowHighWaterMarginProvider); ok {
		margin = provider.ReplayWindowHighWaterMargin()
	}
	if margin == 0 || margin > MaxReplayHighWaterMargin {
		return 0, fmt.Errorf("%w: high-water margin %d is outside 1..%d",
			ErrInvalidReplayState, margin, MaxReplayHighWaterMargin)
	}
	return margin, nil
}

func (w *ReplayWindow) reconcileStoreState() error {
	state, highWaterSet, err := loadReplayState(w.store, w.size)
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
	if _, ok := w.store.(ReplayWindowHighWaterStore); ok && state.Started {
		w.highWater = state.HighestSequence + 1
		w.highWaterSet = highWaterSet
	}
	return nil
}

func loadReplayState(store ReplayWindowStore, size uint8) (ReplayWindowState, bool, error) {
	if highWaterStore, ok := store.(ReplayWindowHighWaterStore); ok {
		highWater, exists, err := highWaterStore.LoadHighWater()
		if err != nil {
			return ReplayWindowState{}, false, fmt.Errorf("%w: load replay high-water mark: %w",
				ErrInternalProcessing, err)
		}
		if exists {
			if highWater == 0 || highWater > maxPartialIV+1 {
				return ReplayWindowState{}, false, fmt.Errorf("%w: %w: invalid high-water mark %d",
					ErrInternalProcessing, ErrInvalidReplayState, highWater)
			}
			return replayWindowStateFromHighWater(highWater, size), true, nil
		}
	}
	state, err := store.Load()
	if err != nil {
		return ReplayWindowState{}, false, fmt.Errorf("%w: load replay state: %w", ErrInternalProcessing, err)
	}
	if err := validateReplayState(size, state); err != nil {
		return ReplayWindowState{}, false, fmt.Errorf("%w: %w", ErrInternalProcessing, err)
	}
	return state, false, nil
}

func replayWindowStateFromHighWater(highWater uint64, size uint8) ReplayWindowState {
	return ReplayWindowState{
		HighestSequence: highWater - 1,
		Bitmap:          replayWindowMask(size),
		Started:         true,
		WindowSize:      size,
	}
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
