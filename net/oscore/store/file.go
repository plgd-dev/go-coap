// Package store provides file-backed OSCORE state stores.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

const (
	fileVersion        = 1
	maxPartialIV       = uint64(1<<40 - 1)
	maxStateBytes      = 4096
	sequenceWindowSize = uint64(256)
)

var (
	ErrInvalidStoreID               = errors.New("OSCORE file store ID must not be empty")
	ErrInvalidReplayHighWaterMargin = errors.New("OSCORE replay high-water margin must be between 1 and 1024")
)

// HighWaterFileStoreOption configures a HighWaterFileStore.
type HighWaterFileStoreOption struct {
	replayHighWaterMargin uint64
}

// WithReplayHighWaterMargin configures how many accepted sequence numbers may
// pass between durable receiver replay cutoff writes. Valid values are 1..1024.
func WithReplayHighWaterMargin(margin uint64) HighWaterFileStoreOption {
	return HighWaterFileStoreOption{replayHighWaterMargin: margin}
}

// HighWaterFileStore persists sender sequence reservations and a receiver
// replay high-water mark. Construct it with the same stable ID whenever the
// corresponding OSCORE context is recreated. Sender sequence guards detect
// individual state-file deletion, corruption, or rollback that causes a
// mismatch, but cannot detect restoring matching older state and guard files.
// Replay cutoff and guard files detect individual deletion or mismatch, and
// interrupted updates are completed from a pending transaction. Deleting or
// rolling back both replay files together is not detectable. These complete-
// state rollbacks require an external monotonic trust anchor to detect.
// The replay cutoff intentionally rejects some previously unobserved requests
// after restart.
//
// On Unix systems, file locking serializes operations across goroutines,
// store instances, and processes. Keep the directory private to the
// application. On other platforms, operations are serialized within the
// process only and directory metadata syncing is unavailable.
type HighWaterFileStore struct {
	dir                   string
	sequencePath          string
	sequenceGuardPath     string
	sequencePendingPath   string
	replayHighWaterPath   string
	replayGuardPath       string
	replayPendingPath     string
	replayHighWaterMargin uint64
	sequenceLock          string
	replayLock            string
	mu                    sync.Mutex
	next                  uint64
	limit                 uint64
	sequenceInitialized   bool
	sequenceOps           sequenceFileOps
	replayOps             sequenceFileOps
}

type sequenceFileOps struct {
	beforeWrite   func(path string) error
	syncFile      func(path string, file *os.File) error
	syncDirectory func(path, dir string) error
	beforeRemove  func(path string) error
}

type sequenceFile struct {
	Version uint8  `json:"version"`
	Next    uint64 `json:"next"`
}

type sequencePendingFile struct {
	Version        uint8        `json:"version"`
	Previous       sequenceFile `json:"previous"`
	PreviousExists bool         `json:"previousExists"`
	Next           sequenceFile `json:"next"`
}

type replayHighWaterFile struct {
	Version uint8  `json:"version"`
	Next    uint64 `json:"next"`
}

type replayHighWaterPendingFile struct {
	Version        uint8               `json:"version"`
	Previous       replayHighWaterFile `json:"previous"`
	PreviousExists bool                `json:"previousExists"`
	Next           replayHighWaterFile `json:"next"`
}

// NewHighWaterFileStore creates a file store in dir. id is an application-
// chosen stable identifier for one OSCORE context; only its SHA-256 digest
// is used in filenames. The directory is created with mode 0700 when
// necessary, and state and lock files are created with mode 0600.
func NewHighWaterFileStore(dir, id string, options ...HighWaterFileStoreOption) (*HighWaterFileStore, error) {
	if id == "" {
		return nil, ErrInvalidStoreID
	}
	if dir == "" {
		return nil, errors.New("OSCORE file store directory is empty")
	}
	replayHighWaterMargin := oscore.DefaultReplayHighWaterMargin
	for _, option := range options {
		replayHighWaterMargin = option.replayHighWaterMargin
	}
	if replayHighWaterMargin == 0 || replayHighWaterMargin > oscore.MaxReplayHighWaterMargin {
		return nil, ErrInvalidReplayHighWaterMargin
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve OSCORE file store directory: %w", err)
	}
	if err := os.MkdirAll(absoluteDir, 0o700); err != nil {
		return nil, fmt.Errorf("create OSCORE file store directory: %w", err)
	}
	digest := sha256.Sum256([]byte(id))
	prefix := filepath.Join(absoluteDir, hex.EncodeToString(digest[:]))
	return &HighWaterFileStore{
		dir:                   absoluteDir,
		sequencePath:          prefix + ".sequence.json",
		sequenceGuardPath:     prefix + ".sequence.guard.json",
		sequencePendingPath:   prefix + ".sequence.pending.json",
		replayHighWaterPath:   prefix + ".replay.high-water.json",
		replayGuardPath:       prefix + ".replay.guard.json",
		replayPendingPath:     prefix + ".replay.pending.json",
		replayHighWaterMargin: replayHighWaterMargin,
		sequenceLock:          prefix + ".sequence.lock",
		replayLock:            prefix + ".replay.lock",
	}, nil
}

// LoadHighWater loads the exclusive replay cutoff used by the batched replay
// store. A missing cutoff and guard means no replay boundary has been persisted.
// If exactly one file is missing or their marks differ, it returns an error.
func (s *HighWaterFileStore) LoadHighWater() (uint64, bool, error) {
	var highWater uint64
	var exists bool
	err := withFileLock(s.replayLock, func() error {
		if err := s.recoverReplayHighWater(); err != nil {
			return err
		}
		var err error
		highWater, exists, err = readReplayHighWaterPair(s.replayHighWaterPath, s.replayGuardPath)
		return err
	})
	if err != nil {
		return 0, false, fmt.Errorf("load OSCORE replay high-water mark: %w", err)
	}
	return highWater, exists, nil
}

// ReplayWindowHighWaterMargin returns the configured persistence margin.
func (s *HighWaterFileStore) ReplayWindowHighWaterMargin() uint64 {
	return s.replayHighWaterMargin
}

// SaveHighWater atomically persists an exclusive replay cutoff. Replay window
// persistence calls this after each configured margin rather than every request.
func (s *HighWaterFileStore) SaveHighWater(highWater uint64) error {
	if highWater == 0 || highWater > maxPartialIV+1 {
		return fmt.Errorf("%w: invalid high-water mark %d", oscore.ErrInvalidReplayState, highWater)
	}
	err := withFileLock(s.replayLock, func() error {
		if err := s.recoverReplayHighWater(); err != nil {
			return err
		}
		current, exists, err := readReplayHighWaterPair(s.replayHighWaterPath, s.replayGuardPath)
		if err != nil {
			return err
		}
		if exists {
			if highWater < current {
				return fmt.Errorf("%w: replay high-water mark moved backward", oscore.ErrInvalidReplayState)
			}
			if highWater == current {
				// A prior rename may have succeeded while its directory sync
				// failed. Re-sync before treating the visible cutoff as durable.
				return s.syncReplayDirectory(s.replayHighWaterPath)
			}
		}
		value := replayHighWaterFile{Version: fileVersion, Next: highWater}
		pending := replayHighWaterPendingFile{
			Version:        fileVersion,
			Previous:       replayHighWaterFile{Version: fileVersion, Next: current},
			PreviousExists: exists,
			Next:           value,
		}
		if err := s.writeReplayStateFile(s.replayPendingPath, pending); err != nil {
			return fmt.Errorf("persist replay high-water transaction: %w", err)
		}
		if err := s.writeReplayStateFile(s.replayGuardPath, value); err != nil {
			return fmt.Errorf("persist replay high-water guard: %w", err)
		}
		if err := s.writeReplayStateFile(s.replayHighWaterPath, value); err != nil {
			return fmt.Errorf("persist replay high-water mark: %w", err)
		}
		if err := s.removeReplayPending(); err != nil {
			return fmt.Errorf("complete replay high-water transaction: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save OSCORE replay high-water mark: %w", err)
	}
	return nil
}

// ResetReplayState removes the replay cutoff and its guard. Call it only when
// deliberately replacing the associated OSCORE security context, with no
// active Context using this store. Resetting while reusing the same keys allows
// previously accepted requests to be replayed.
func (s *HighWaterFileStore) ResetReplayState() error {
	err := withFileLock(s.replayLock, func() error {
		for _, path := range []string{s.replayHighWaterPath, s.replayGuardPath, s.replayPendingPath} {
			if s.replayOps.beforeRemove != nil {
				if err := s.replayOps.beforeRemove(path); err != nil {
					return err
				}
			}
			if err := os.Remove(path); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				continue
			}
		}
		// Sync even if the files are already absent: a previous reset may
		// have removed them but failed to persist that directory update.
		return s.syncReplayDirectory(s.replayHighWaterPath)
	})
	if err != nil {
		return fmt.Errorf("reset OSCORE replay state: %w", err)
	}
	return nil
}

// Load supplies the empty ReplayWindowStore fallback when no high-water mark
// has been persisted. Use LoadHighWater to read the persisted cutoff.
func (s *HighWaterFileStore) Load() (oscore.ReplayWindowState, error) {
	return oscore.ReplayWindowState{}, nil
}

// Save persists a conservative exclusive cutoff instead of the replay bitmap.
func (s *HighWaterFileStore) Save(state oscore.ReplayWindowState) error {
	if !state.Started || state.HighestSequence > maxPartialIV {
		return fmt.Errorf("%w: invalid replay state", oscore.ErrInvalidReplayState)
	}
	return s.SaveHighWater(min(state.HighestSequence+s.replayHighWaterMargin, maxPartialIV+1))
}

// Reserve returns a sender sequence number from a durably reserved window.
// Unused values in a window are skipped when the store is recreated.
func (s *HighWaterFileStore) Reserve() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next < s.limit {
		reserved := s.next
		s.next++
		return reserved, nil
	}

	var reserved, limit uint64
	err := withFileLock(s.sequenceLock, func() error {
		state, err := s.loadSequenceState()
		if err != nil {
			return err
		}
		if state.Next > maxPartialIV {
			return oscore.ErrSenderSequenceExhausted
		}
		reserved = state.Next
		remaining := maxPartialIV + 1 - reserved
		windowSize := min(sequenceWindowSize, remaining)
		limit = reserved + windowSize
		next := sequenceFile{Version: fileVersion, Next: limit}
		pending := sequencePendingFile{
			Version:        fileVersion,
			Previous:       sequenceFile{Version: fileVersion, Next: reserved},
			PreviousExists: reserved != 0,
			Next:           next,
		}
		if err := s.writeSequenceState(s.sequencePendingPath, pending); err != nil {
			return fmt.Errorf("persist sender sequence transaction: %w", err)
		}
		if err := s.writeSequenceState(s.sequenceGuardPath, next); err != nil {
			return fmt.Errorf("persist sender sequence guard: %w", err)
		}
		if err := s.writeSequenceState(s.sequencePath, next); err != nil {
			return fmt.Errorf("persist sender sequence state: %w", err)
		}
		if err := s.removeSequencePending(); err != nil {
			return fmt.Errorf("complete sender sequence transaction: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reserve OSCORE sender sequence: %w", err)
	}
	s.next = reserved + 1
	s.limit = limit
	s.sequenceInitialized = true
	return reserved, nil
}

func (s *HighWaterFileStore) loadSequenceState() (sequenceFile, error) {
	if err := s.recoverSequenceReservation(); err != nil {
		return sequenceFile{}, err
	}
	state, stateExists, err := readSequenceWithExistence(s.sequencePath)
	if err != nil {
		return sequenceFile{}, err
	}
	guard, guardExists, err := readSequenceWithExistence(s.sequenceGuardPath)
	if err != nil {
		return sequenceFile{}, err
	}
	if !stateExists && !guardExists {
		if s.sequenceInitialized {
			return sequenceFile{}, errors.New("OSCORE sender sequence state and rollback guard are missing after initialization")
		}
		return sequenceFile{Version: fileVersion}, nil
	}
	if !stateExists || !guardExists {
		return sequenceFile{}, errors.New("OSCORE sender sequence state or rollback guard is missing")
	}
	if state != guard {
		return sequenceFile{}, errors.New("OSCORE sender sequence state differs from its rollback guard")
	}
	s.sequenceInitialized = true
	return state, nil
}

func (s *HighWaterFileStore) recoverSequenceReservation() error {
	pending, pendingExists, err := readSequencePending(s.sequencePendingPath)
	if err != nil {
		return err
	}
	if !pendingExists {
		return nil
	}
	if err = validateSequencePending(pending); err != nil {
		return err
	}
	state, stateExists, err := readSequenceWithExistence(s.sequencePath)
	if err != nil {
		return err
	}
	guard, guardExists, err := readSequenceWithExistence(s.sequenceGuardPath)
	if err != nil {
		return err
	}
	if err := validateSequenceTransactionProgress(pending, state, stateExists, guard, guardExists); err != nil {
		return err
	}
	if stateExists && guardExists {
		s.sequenceInitialized = true
	}
	return s.finishSequenceReservationRecovery(pending, state, stateExists, guard, guardExists)
}

func (s *HighWaterFileStore) recoverReplayHighWater() error {
	pending, pendingExists, err := readReplayHighWaterPending(s.replayPendingPath)
	if err != nil {
		return err
	}
	if !pendingExists {
		return nil
	}
	if err = validateReplayHighWaterPending(pending); err != nil {
		return err
	}
	mark, markExists, err := readReplayHighWater(s.replayHighWaterPath)
	if err != nil {
		return err
	}
	guard, guardExists, err := readReplayHighWater(s.replayGuardPath)
	if err != nil {
		return err
	}
	if !replayHighWaterTransactionProgressIsValid(pending, mark, markExists, guard, guardExists) {
		return errors.New("OSCORE replay high-water files do not match pending transaction")
	}
	if !guardExists || guard != pending.Next.Next {
		if err := s.writeReplayStateFile(s.replayGuardPath, pending.Next); err != nil {
			return fmt.Errorf("recover replay high-water guard: %w", err)
		}
	}
	if !markExists || mark != pending.Next.Next {
		if err := s.writeReplayStateFile(s.replayHighWaterPath, pending.Next); err != nil {
			return fmt.Errorf("recover replay high-water mark: %w", err)
		}
	}
	if err := s.removeReplayPending(); err != nil {
		return fmt.Errorf("complete replay high-water recovery: %w", err)
	}
	return nil
}

func validateReplayHighWaterPending(pending replayHighWaterPendingFile) error {
	if pending.Version != fileVersion || pending.Previous.Version != fileVersion ||
		pending.Next.Version != fileVersion ||
		pending.Next.Next == 0 || pending.Next.Next > maxPartialIV+1 {
		return errors.New("invalid OSCORE replay high-water transaction")
	}
	if pending.PreviousExists {
		if pending.Previous.Next == 0 || pending.Previous.Next >= pending.Next.Next {
			return errors.New("invalid OSCORE replay high-water transaction")
		}
	} else if pending.Previous.Next != 0 {
		return errors.New("invalid OSCORE replay high-water transaction")
	}
	return nil
}

func replayHighWaterTransactionProgressIsValid(
	pending replayHighWaterPendingFile,
	mark uint64,
	markExists bool,
	guard uint64,
	guardExists bool,
) bool {
	if pending.PreviousExists {
		return (!markExists || mark == pending.Previous.Next || mark == pending.Next.Next) &&
			(!guardExists || guard == pending.Previous.Next || guard == pending.Next.Next)
	}
	return (!markExists || mark == pending.Next.Next) &&
		(!guardExists || guard == pending.Next.Next)
}

func (s *HighWaterFileStore) removeReplayPending() error {
	if s.replayOps.beforeRemove != nil {
		if err := s.replayOps.beforeRemove(s.replayPendingPath); err != nil {
			return err
		}
	}
	if err := os.Remove(s.replayPendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.syncReplayDirectory(s.replayPendingPath)
}

func (s *HighWaterFileStore) finishSequenceReservationRecovery(
	pending sequencePendingFile,
	state sequenceFile,
	stateExists bool,
	guard sequenceFile,
	guardExists bool,
) error {
	if !guardExists || guard != pending.Next {
		if err := s.writeSequenceState(s.sequenceGuardPath, pending.Next); err != nil {
			return fmt.Errorf("recover sender sequence guard: %w", err)
		}
	}
	if !stateExists || state != pending.Next {
		if err := s.writeSequenceState(s.sequencePath, pending.Next); err != nil {
			return fmt.Errorf("recover sender sequence state: %w", err)
		}
	}
	s.sequenceInitialized = true
	if err := s.removeSequencePending(); err != nil {
		return fmt.Errorf("complete sender sequence recovery: %w", err)
	}
	return nil
}

func validateSequencePending(pending sequencePendingFile) error {
	if pending.Version != fileVersion {
		return errors.New("invalid OSCORE sender sequence transaction version")
	}
	if err := validateSequence(pending.Previous); err != nil {
		return err
	}
	if err := validateSequence(pending.Next); err != nil {
		return err
	}
	if !pending.PreviousExists && pending.Previous.Next != 0 ||
		pending.Next.Next <= pending.Previous.Next ||
		pending.Next.Next-pending.Previous.Next > sequenceWindowSize {
		return errors.New("invalid OSCORE sender sequence transaction")
	}
	expectedWindow := min(sequenceWindowSize, maxPartialIV+1-pending.Previous.Next)
	if pending.Next.Next-pending.Previous.Next != expectedWindow {
		return errors.New("invalid OSCORE sender sequence reservation window")
	}
	return nil
}

func validateSequenceTransactionProgress(
	pending sequencePendingFile,
	state sequenceFile,
	stateExists bool,
	guard sequenceFile,
	guardExists bool,
) error {
	if sequenceTransactionProgressIsValid(pending, state, stateExists, guard, guardExists) {
		return nil
	}
	return errors.New("OSCORE sender sequence files do not match pending transaction")
}

func sequenceTransactionProgressIsValid(
	pending sequencePendingFile,
	state sequenceFile,
	stateExists bool,
	guard sequenceFile,
	guardExists bool,
) bool {
	stateIsPrevious := pending.PreviousExists && stateExists && state == pending.Previous
	stateIsNext := stateExists && state == pending.Next
	guardIsPrevious := pending.PreviousExists && guardExists && guard == pending.Previous
	guardIsNext := guardExists && guard == pending.Next
	return validPreviousSequenceTransaction(pending, stateIsPrevious, guardIsPrevious, guardIsNext) ||
		stateIsNext && guardIsNext ||
		validFreshSequenceTransaction(pending, stateExists, stateIsNext, guardExists, guardIsNext)
}

func validPreviousSequenceTransaction(
	pending sequencePendingFile,
	stateIsPrevious bool,
	guardIsPrevious bool,
	guardIsNext bool,
) bool {
	return pending.PreviousExists && stateIsPrevious && (guardIsPrevious || guardIsNext)
}

func validFreshSequenceTransaction(
	pending sequencePendingFile,
	stateExists bool,
	stateIsNext bool,
	guardExists bool,
	guardIsNext bool,
) bool {
	return !pending.PreviousExists && pending.Previous.Next == 0 &&
		(!stateExists || stateIsNext) && (!guardExists || guardIsNext)
}

func (s *HighWaterFileStore) removeSequencePending() error {
	if s.sequenceOps.beforeRemove != nil {
		if err := s.sequenceOps.beforeRemove(s.sequencePendingPath); err != nil {
			return err
		}
	}
	if err := os.Remove(s.sequencePendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.sequenceOps.syncDirectory != nil {
		return s.sequenceOps.syncDirectory(s.sequencePendingPath, s.dir)
	}
	return syncDirectory(s.dir)
}

func (s *HighWaterFileStore) writeSequenceState(path string, value any) error {
	if s.sequenceOps.beforeWrite != nil {
		if err := s.sequenceOps.beforeWrite(path); err != nil {
			return err
		}
	}
	return writeStateFileWithSync(s.dir, path, value, func() error {
		if s.sequenceOps.syncDirectory != nil {
			return s.sequenceOps.syncDirectory(path, s.dir)
		}
		return syncDirectory(s.dir)
	})
}

func (s *HighWaterFileStore) writeReplayStateFile(path string, value any) error {
	if s.replayOps.beforeWrite != nil {
		if err := s.replayOps.beforeWrite(path); err != nil {
			return err
		}
	}
	return writeStateFileWithHooks(s.dir, path, value, func(file *os.File) error {
		if s.replayOps.syncFile != nil {
			return s.replayOps.syncFile(path, file)
		}
		return file.Sync()
	}, func() error {
		return s.syncReplayDirectory(path)
	})
}

func (s *HighWaterFileStore) syncReplayDirectory(path string) error {
	if s.replayOps.syncDirectory != nil {
		return s.replayOps.syncDirectory(path, s.dir)
	}
	return syncDirectory(s.dir)
}

func validateSequence(state sequenceFile) error {
	if state.Version != fileVersion || state.Next > maxPartialIV+1 ||
		state.Next%sequenceWindowSize != 0 {
		return errors.New("invalid OSCORE sender sequence file")
	}
	return nil
}

func readSequenceWithExistence(path string) (sequenceFile, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return sequenceFile{}, false, err
	}
	if data == nil {
		return sequenceFile{}, false, nil
	}
	var state sequenceFile
	if err := json.Unmarshal(data, &state); err != nil {
		return sequenceFile{}, false, fmt.Errorf("decode sender sequence file: %w", err)
	}
	if err := validateSequence(state); err != nil || state.Next == 0 {
		return sequenceFile{}, false, errors.New("invalid OSCORE sender sequence file")
	}
	return state, true, nil
}

func readSequencePending(path string) (sequencePendingFile, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return sequencePendingFile{}, false, err
	}
	if data == nil {
		return sequencePendingFile{}, false, nil
	}
	var pending sequencePendingFile
	if err := json.Unmarshal(data, &pending); err != nil {
		return sequencePendingFile{}, false, fmt.Errorf("decode sender sequence transaction file: %w", err)
	}
	return pending, true, nil
}

func readReplayHighWater(path string) (uint64, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return 0, false, err
	}
	if data == nil {
		return 0, false, nil
	}
	var state replayHighWaterFile
	if err := json.Unmarshal(data, &state); err != nil {
		return 0, false, fmt.Errorf("decode replay high-water file: %w", err)
	}
	if state.Version != fileVersion || state.Next == 0 || state.Next > maxPartialIV+1 {
		return 0, false, fmt.Errorf("%w: invalid replay high-water file", oscore.ErrInvalidReplayState)
	}
	return state.Next, true, nil
}

func readReplayHighWaterPending(path string) (replayHighWaterPendingFile, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return replayHighWaterPendingFile{}, false, err
	}
	if data == nil {
		return replayHighWaterPendingFile{}, false, nil
	}
	var pending replayHighWaterPendingFile
	if err := json.Unmarshal(data, &pending); err != nil {
		return replayHighWaterPendingFile{}, false, fmt.Errorf("decode replay high-water transaction file: %w", err)
	}
	return pending, true, nil
}

func readReplayHighWaterPair(markPath, guardPath string) (uint64, bool, error) {
	mark, markExists, err := readReplayHighWater(markPath)
	if err != nil {
		return 0, false, err
	}
	guard, guardExists, err := readReplayHighWater(guardPath)
	if err != nil {
		return 0, false, err
	}
	if !markExists && !guardExists {
		return 0, false, nil
	}
	if !markExists || !guardExists {
		return 0, false, errors.New("OSCORE replay high-water mark or guard is missing")
	}
	if mark != guard {
		return 0, false, errors.New("OSCORE replay high-water mark differs from its guard")
	}
	return mark, true, nil
}

func readStateFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStateBytes {
		return nil, fmt.Errorf("OSCORE state file exceeds %d bytes", maxStateBytes)
	}
	return data, nil
}

func writeStateFile(dir, path string, value any) error {
	return writeStateFileWithSync(dir, path, value, func() error {
		return syncDirectory(dir)
	})
}

func writeStateFileWithSync(dir, path string, value any, syncDir func() error) error {
	return writeStateFileWithHooks(dir, path, value, nil, syncDir)
}

func writeStateFileWithHooks(dir, path string, value any, syncFile func(*os.File) error, syncDir func() error) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode OSCORE state: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".oscore-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if syncFile == nil {
		syncFile = func(file *os.File) error { return file.Sync() }
	}
	if err := syncFile(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := syncDir(); err != nil {
		return err
	}
	return nil
}
