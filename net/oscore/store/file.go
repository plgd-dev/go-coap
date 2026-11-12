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

var ErrInvalidStoreID = errors.New("OSCORE file store ID must not be empty")

// FileStore persists an OSCORE sender sequence and receiver replay window in
// separate files under Dir. Construct it with the same stable ID whenever the
// corresponding OSCORE context is recreated.
// Sender sequence and replay guards detect individual state-file deletion,
// corruption, or rollback. Pending transactions let the store finish
// interrupted updates. Backups must preserve each state file, its guard, and
// any pending transaction together; rollback of the complete store directory
// or a state file and its guard together cannot be detected without an
// external monotonic trust anchor. Existing initialized state without its
// guard is rejected and must be migrated from a trusted copy.
//
// On Unix systems, file locking serializes operations across goroutines,
// FileStore instances, and processes. Keep the directory private to the
// application. On other platforms, operations are serialized within the
// process only and directory metadata syncing is unavailable.
type FileStore struct {
	dir                 string
	sequencePath        string
	sequenceGuardPath   string
	sequencePendingPath string
	replayPath          string
	replayGuardPath     string
	replayPendingPath   string
	sequenceLock        string
	replayLock          string
	mu                  sync.Mutex
	next                uint64
	limit               uint64
	sequenceInitialized bool
	sequenceOps         sequenceFileOps
}

type sequenceFileOps struct {
	beforeWrite   func(path string) error
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

type replayFile struct {
	Version uint8 `json:"version"`
	oscore.ReplayWindowState
}

type replayPendingFile struct {
	Version  uint8 `json:"version"`
	Previous oscore.ReplayWindowState
	Next     oscore.ReplayWindowState
}

// NewFileStore creates a file store in dir. id is an application-chosen,
// stable identifier for one OSCORE context; only its SHA-256 digest is used
// in filenames. The directory is created with mode 0700 when necessary, and
// state and lock files are created with mode 0600.
func NewFileStore(dir, id string) (*FileStore, error) {
	if id == "" {
		return nil, ErrInvalidStoreID
	}
	if dir == "" {
		return nil, errors.New("OSCORE file store directory is empty")
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
	return &FileStore{
		dir:                 absoluteDir,
		sequencePath:        prefix + ".sequence.json",
		sequenceGuardPath:   prefix + ".sequence.guard.json",
		sequencePendingPath: prefix + ".sequence.pending.json",
		replayPath:          prefix + ".replay.json",
		replayGuardPath:     prefix + ".replay.guard.json",
		replayPendingPath:   prefix + ".replay.pending.json",
		sequenceLock:        prefix + ".sequence.lock",
		replayLock:          prefix + ".replay.lock",
	}, nil
}

// Reserve returns a sender sequence number from a durably reserved window.
// Unused values in a window are skipped when the store is recreated.
func (s *FileStore) Reserve() (uint64, error) {
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

func (s *FileStore) loadSequenceState() (sequenceFile, error) {
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

func (s *FileStore) recoverSequenceReservation() error {
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

func (s *FileStore) finishSequenceReservationRecovery(
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

func (s *FileStore) removeSequencePending() error {
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

func (s *FileStore) writeSequenceState(path string, value any) error {
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

// Load returns the persisted replay state, or an empty state when this
// context has not yet accepted a protected message.
func (s *FileStore) Load() (oscore.ReplayWindowState, error) {
	var state oscore.ReplayWindowState
	err := withFileLock(s.replayLock, func() error {
		var err error
		state, err = s.loadReplayState()
		return err
	})
	if err != nil {
		return oscore.ReplayWindowState{}, fmt.Errorf("load OSCORE replay state: %w", err)
	}
	return state, nil
}

// Save durably records an authenticated replay-window advance. Stale or
// conflicting updates are rejected so multiple active contexts cannot
// silently roll persisted replay state backward.
func (s *FileStore) Save(state oscore.ReplayWindowState) error {
	if err := validateReplayState(state); err != nil {
		return err
	}
	err := withFileLock(s.replayLock, func() error {
		current, err := s.loadReplayState()
		if err != nil {
			return err
		}
		if err := validateReplayAdvance(current, state); err != nil {
			return err
		}
		next := replayFile{
			Version:           fileVersion,
			ReplayWindowState: state,
		}
		pending := replayPendingFile{
			Version:  fileVersion,
			Previous: current,
			Next:     state,
		}
		if err := writeStateFile(s.dir, s.replayPendingPath, pending); err != nil {
			return fmt.Errorf("persist replay state transaction: %w", err)
		}
		if err := writeStateFile(s.dir, s.replayGuardPath, next); err != nil {
			return err
		}
		if err := writeStateFile(s.dir, s.replayPath, next); err != nil {
			return err
		}
		return s.removeReplayPending()
	})
	if err != nil {
		return fmt.Errorf("save OSCORE replay state: %w", err)
	}
	return nil
}

func (s *FileStore) loadReplayState() (oscore.ReplayWindowState, error) {
	if err := s.recoverReplaySave(); err != nil {
		return oscore.ReplayWindowState{}, err
	}
	current, err := readReplay(s.replayPath)
	if err != nil {
		return oscore.ReplayWindowState{}, err
	}
	guard, guardExists, err := readReplayWithExistence(s.replayGuardPath)
	if err != nil {
		return oscore.ReplayWindowState{}, err
	}
	if !guardExists {
		if current.Started {
			return oscore.ReplayWindowState{}, fmt.Errorf("%w: replay rollback guard is missing", oscore.ErrInvalidReplayState)
		}
		return current, nil
	}
	if replayStatesEqual(current, guard) {
		return current, nil
	}
	return oscore.ReplayWindowState{}, fmt.Errorf("%w: replay state differs from its rollback guard", oscore.ErrInvalidReplayState)
}

func (s *FileStore) recoverReplaySave() error {
	pending, pendingExists, err := readReplayPending(s.replayPendingPath)
	if err != nil {
		return err
	}
	if !pendingExists {
		return nil
	}
	if err = validateReplayPending(pending); err != nil {
		return err
	}

	current, _, err := readReplayWithExistence(s.replayPath)
	if err != nil {
		return err
	}
	guard, guardExists, err := readReplayWithExistence(s.replayGuardPath)
	if err != nil {
		return err
	}
	if err := validateReplayTransactionProgress(pending, current, guard, guardExists); err != nil {
		return err
	}

	next := replayFile{Version: fileVersion, ReplayWindowState: pending.Next}
	if !replayStatesEqual(guard, pending.Next) || !guardExists {
		if err := writeStateFile(s.dir, s.replayGuardPath, next); err != nil {
			return fmt.Errorf("recover OSCORE replay guard: %w", err)
		}
	}
	if !replayStatesEqual(current, pending.Next) {
		if err := writeStateFile(s.dir, s.replayPath, next); err != nil {
			return fmt.Errorf("recover OSCORE replay state: %w", err)
		}
	}
	if err := s.removeReplayPending(); err != nil {
		return fmt.Errorf("complete OSCORE replay recovery: %w", err)
	}
	return nil
}

func validateReplayPending(pending replayPendingFile) error {
	if pending.Version != fileVersion {
		return fmt.Errorf("%w: unsupported replay transaction version %d", oscore.ErrInvalidReplayState, pending.Version)
	}
	if err := validateReplayState(pending.Previous); err != nil {
		return err
	}
	if err := validateReplayState(pending.Next); err != nil {
		return err
	}
	if err := validateReplayAdvance(pending.Previous, pending.Next); err != nil {
		return fmt.Errorf("%w: invalid replay transaction", oscore.ErrInvalidReplayState)
	}
	return nil
}

func validateReplayTransactionProgress(
	pending replayPendingFile,
	current oscore.ReplayWindowState,
	guard oscore.ReplayWindowState,
	guardExists bool,
) error {
	replayIsPrevious := replayStatesEqual(current, pending.Previous)
	replayIsNext := replayStatesEqual(current, pending.Next)
	guardIsPrevious := guardExists && replayStatesEqual(guard, pending.Previous)
	guardIsNext := guardExists && replayStatesEqual(guard, pending.Next)
	if replayIsPrevious && (guardIsPrevious || guardIsNext) ||
		replayIsNext && guardIsNext ||
		!guardExists && replayIsPrevious && !pending.Previous.Started {
		return nil
	}
	return fmt.Errorf("%w: replay files do not match pending transaction", oscore.ErrInvalidReplayState)
}

func (s *FileStore) removeReplayPending() error {
	if err := os.Remove(s.replayPendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(s.dir)
}

func readReplayPending(path string) (replayPendingFile, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return replayPendingFile{}, false, err
	}
	if data == nil {
		return replayPendingFile{}, false, nil
	}
	var pending replayPendingFile
	if err := json.Unmarshal(data, &pending); err != nil {
		return replayPendingFile{}, false, fmt.Errorf("decode replay transaction file: %w", err)
	}
	return pending, true, nil
}

func replayStatesEqual(left, right oscore.ReplayWindowState) bool {
	return left.HighestSequence == right.HighestSequence &&
		left.Bitmap == right.Bitmap &&
		left.Started == right.Started &&
		left.WindowSize == right.WindowSize
}

func validateReplayState(state oscore.ReplayWindowState) error {
	if !state.Started {
		if state.HighestSequence != 0 || state.Bitmap != 0 || state.WindowSize != 0 {
			return fmt.Errorf("%w: unstarted state contains sequence data", oscore.ErrInvalidReplayState)
		}
		return nil
	}
	if state.HighestSequence > maxPartialIV || state.Bitmap&1 == 0 ||
		state.WindowSize == 0 || state.WindowSize > 64 {
		return fmt.Errorf("%w: invalid sequence, bitmap, or window size", oscore.ErrInvalidReplayState)
	}
	if state.WindowSize < 64 && state.Bitmap>>state.WindowSize != 0 {
		return fmt.Errorf("%w: bitmap exceeds configured window size", oscore.ErrInvalidReplayState)
	}
	return nil
}

func validateReplayAdvance(current, next oscore.ReplayWindowState) error {
	if !current.Started {
		return nil
	}
	if !next.Started || current.WindowSize != next.WindowSize ||
		next.HighestSequence < current.HighestSequence {
		return oscore.ErrReplay
	}
	mask := ^uint64(0)
	if current.WindowSize < 64 {
		mask = (uint64(1) << current.WindowSize) - 1
	}
	if next.HighestSequence == current.HighestSequence {
		if next.Bitmap == current.Bitmap || next.Bitmap&current.Bitmap != current.Bitmap {
			return oscore.ErrReplay
		}
		return nil
	}
	shift := next.HighestSequence - current.HighestSequence
	required := uint64(0)
	if shift < uint64(current.WindowSize) {
		required = (current.Bitmap << shift) & mask
	}
	if next.Bitmap&required != required {
		return oscore.ErrReplay
	}
	return nil
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

func readReplay(path string) (oscore.ReplayWindowState, error) {
	state, _, err := readReplayWithExistence(path)
	return state, err
}

func readReplayWithExistence(path string) (oscore.ReplayWindowState, bool, error) {
	data, err := readStateFile(path)
	if err != nil {
		return oscore.ReplayWindowState{}, false, err
	}
	if data == nil {
		return oscore.ReplayWindowState{}, false, nil
	}
	var state replayFile
	if err := json.Unmarshal(data, &state); err != nil {
		return oscore.ReplayWindowState{}, false, fmt.Errorf("decode replay state file: %w", err)
	}
	if state.Version != fileVersion {
		return oscore.ReplayWindowState{}, false, fmt.Errorf("unsupported OSCORE replay state file version %d", state.Version)
	}
	if err := validateReplayState(state.ReplayWindowState); err != nil {
		return oscore.ReplayWindowState{}, false, err
	}
	return state.ReplayWindowState, true, nil
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
	if err := temporary.Sync(); err != nil {
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
