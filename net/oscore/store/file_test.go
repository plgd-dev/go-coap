package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

func TestFileStoreSequenceReservationsAcrossInstances(t *testing.T) {
	first, err := NewFileStore(t.TempDir(), "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(first.dir, "peer-a")
	if err != nil {
		t.Fatal(err)
	}

	const count = 64
	values := make(chan uint64, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := first
			if i%2 != 0 {
				store = second
			}
			value, reserveErr := store.Reserve()
			if reserveErr != nil {
				t.Errorf("reserve sequence: %v", reserveErr)
				return
			}
			values <- value
		}(i)
	}
	wg.Wait()
	close(values)

	seen := make(map[uint64]bool, count)
	for value := range values {
		if seen[value] {
			t.Fatalf("sequence %d was reserved more than once", value)
		}
		seen[value] = true
	}
	if len(seen) != count {
		t.Fatalf("reserved %d values, want %d", len(seen), count)
	}
	maxReserved := uint64(0)
	for value := range seen {
		maxReserved = max(maxReserved, value)
	}
	restarted, err := NewFileStore(first.dir, "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if next <= maxReserved {
		t.Fatalf("restarted sequence = %d, want greater than %d", next, maxReserved)
	}
}

func TestFileStoreSequenceWindowSkipsUnusedValuesAfterRestart(t *testing.T) {
	first, err := NewFileStore(t.TempDir(), "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := first.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if initial != 0 {
		t.Fatalf("initial sequence = %d, want 0", initial)
	}
	nextInWindow, err := first.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if nextInWindow != 1 {
		t.Fatalf("next sequence in window = %d, want 1", nextInWindow)
	}

	restarted, err := NewFileStore(first.dir, "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	nextAfterRestart, err := restarted.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if nextAfterRestart != sequenceWindowSize {
		t.Fatalf("sequence after restart = %d, want %d", nextAfterRestart, sequenceWindowSize)
	}
}

func TestFileStoreRejectsMissingSenderSequenceStateOrGuard(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*FileStore) string
	}{
		{name: "sequence file", path: func(s *FileStore) string { return s.sequencePath }},
		{name: "sequence guard", path: func(s *FileStore) string { return s.sequenceGuardPath }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir(), "missing-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, reserveErr := store.Reserve(); reserveErr != nil {
				t.Fatal(reserveErr)
			}
			if removeErr := os.Remove(test.path(store)); removeErr != nil {
				t.Fatal(removeErr)
			}
			if value, reserveErr := store.Reserve(); reserveErr != nil || value != 1 {
				t.Fatalf("active reservation after deleting %s = %d, %v; want 1", test.name, value, reserveErr)
			}
			restarted, err := NewFileStore(store.dir, "missing-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			if value, err := restarted.Reserve(); err == nil {
				t.Fatalf("reservation after deleting %s returned %d", test.name, value)
			}
		})
	}
}

func TestFileStoreSequencePersistenceFailureDoesNotReturnAValue(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "sender-sequence-storage-failure")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := store.Reserve(); err != nil || value != 0 {
		t.Fatalf("first reservation = %d, %v; want 0", value, err)
	}
	for range sequenceWindowSize - 1 {
		if _, err := store.Reserve(); err != nil {
			t.Fatal(err)
		}
	}
	offlineDir := filepath.Join(t.TempDir(), "offline")
	if err := os.Rename(store.dir, offlineDir); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Reserve(); err == nil {
		t.Fatalf("reservation returned %d while durable storage was unavailable", value)
	}
	if err := os.Rename(offlineDir, store.dir); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Reserve(); err != nil || value != sequenceWindowSize {
		t.Fatalf("reservation after storage recovery = %d, %v; want %d", value, err, sequenceWindowSize)
	}
}

func TestFileStoreSequencePersistenceFaultInjection(t *testing.T) {
	tests := []struct {
		name         string
		inject       func(*FileStore, error)
		retryInPlace bool
	}{
		{
			name: "pending transaction write",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.beforeWrite = func(path string) error {
					if path == store.sequencePendingPath {
						return failure
					}
					return nil
				}
			},
			retryInPlace: true,
		},
		{
			name: "sequence guard write",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.beforeWrite = func(path string) error {
					if path == store.sequenceGuardPath {
						return failure
					}
					return nil
				}
			},
		},
		{
			name: "sequence state write",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.beforeWrite = func(path string) error {
					if path == store.sequencePath {
						return failure
					}
					return nil
				}
			},
		},
		{
			name: "pending directory sync",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.syncDirectory = func(path, dir string) error {
					if path == store.sequencePendingPath {
						return failure
					}
					return syncDirectory(dir)
				}
			},
		},
		{
			name: "guard directory sync",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.syncDirectory = func(path, dir string) error {
					if path == store.sequenceGuardPath {
						return failure
					}
					return syncDirectory(dir)
				}
			},
		},
		{
			name: "sequence directory sync",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.syncDirectory = func(path, dir string) error {
					if path == store.sequencePath {
						return failure
					}
					return syncDirectory(dir)
				}
			},
		},
		{
			name: "pending removal",
			inject: func(store *FileStore, failure error) {
				store.sequenceOps.beforeRemove = func(string) error {
					return failure
				}
			},
		},
		{
			name: "pending removal directory sync",
			inject: func(store *FileStore, failure error) {
				pendingSyncs := 0
				store.sequenceOps.syncDirectory = func(path, dir string) error {
					if path == store.sequencePendingPath {
						pendingSyncs++
						if pendingSyncs == 2 {
							return failure
						}
					}
					return syncDirectory(dir)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			id := "sequence-fault-" + test.name
			store, err := NewFileStore(dir, id)
			if err != nil {
				t.Fatal(err)
			}
			injectedFailure := errors.New("injected sender sequence persistence failure")
			test.inject(store, injectedFailure)
			if value, reserveErr := store.Reserve(); !errors.Is(reserveErr, injectedFailure) {
				t.Fatalf("reservation = %d, %v; want injected failure", value, reserveErr)
			}

			if test.retryInPlace {
				store.sequenceOps = sequenceFileOps{}
				if value, reserveErr := store.Reserve(); reserveErr != nil || value != 0 {
					t.Fatalf("reservation after pre-transaction failure = %d, %v; want 0", value, reserveErr)
				}
				return
			}
			restarted, err := NewFileStore(dir, id)
			if err != nil {
				t.Fatal(err)
			}
			if value, err := restarted.Reserve(); err != nil || value != sequenceWindowSize {
				t.Fatalf("reservation after interrupted transaction = %d, %v; want %d", value, err, sequenceWindowSize)
			}
		})
	}
}

func TestFileStoreRejectsRestoredSenderSequenceSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "restored-sender-sequence")
	if err != nil {
		t.Fatal(err)
	}
	if _, reserveErr := store.Reserve(); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	oldSnapshot, err := os.ReadFile(store.sequencePath)
	if err != nil {
		t.Fatal(err)
	}
	for range sequenceWindowSize - 1 {
		if _, reserveErr := store.Reserve(); reserveErr != nil {
			t.Fatal(reserveErr)
		}
	}
	if value, reserveErr := store.Reserve(); reserveErr != nil || value != sequenceWindowSize {
		t.Fatalf("reservation at next window = %d, %v; want %d", value, reserveErr, sequenceWindowSize)
	}
	if writeErr := os.WriteFile(store.sequencePath, oldSnapshot, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	restarted, err := NewFileStore(dir, "restored-sender-sequence")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := restarted.Reserve(); err == nil {
		t.Fatalf("reservation after restoring old sequence snapshot returned %d", value)
	}
}

func TestFileStoreRecoversInterruptedSequenceReservation(t *testing.T) {
	stages := []struct {
		name          string
		initialized   bool
		writeGuard    bool
		writeSequence bool
	}{
		{name: "after pending transaction"},
		{name: "after guard", writeGuard: true},
		{name: "after sequence", writeGuard: true, writeSequence: true},
		{name: "existing state after pending", initialized: true},
		{name: "existing state after guard", initialized: true, writeGuard: true},
		{name: "existing state after sequence", initialized: true, writeGuard: true, writeSequence: true},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewFileStore(dir, "interrupted-sequence-"+stage.name)
			if err != nil {
				t.Fatal(err)
			}
			previous := sequenceFile{Version: fileVersion}
			previousExists := stage.initialized
			if stage.initialized {
				previous.Next = sequenceWindowSize
				if writeErr := writeStateFile(dir, store.sequencePath, previous); writeErr != nil {
					t.Fatal(writeErr)
				}
				if writeErr := writeStateFile(dir, store.sequenceGuardPath, previous); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			next := sequenceFile{Version: fileVersion, Next: previous.Next + sequenceWindowSize}
			pending := sequencePendingFile{
				Version:        fileVersion,
				Previous:       previous,
				PreviousExists: previousExists,
				Next:           next,
			}
			if writeErr := writeStateFile(dir, store.sequencePendingPath, pending); writeErr != nil {
				t.Fatal(writeErr)
			}
			if stage.writeGuard {
				if writeErr := writeStateFile(dir, store.sequenceGuardPath, next); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if stage.writeSequence {
				if writeErr := writeStateFile(dir, store.sequencePath, next); writeErr != nil {
					t.Fatal(writeErr)
				}
			}

			restarted, err := NewFileStore(dir, "interrupted-sequence-"+stage.name)
			if err != nil {
				t.Fatal(err)
			}
			value, err := restarted.Reserve()
			if err != nil {
				t.Fatalf("reserve after recovering interrupted transaction: %v", err)
			}
			if value != next.Next {
				t.Fatalf("reservation after recovery = %d, want %d", value, next.Next)
			}
			if _, err := os.Stat(store.sequencePendingPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending sequence transaction remains after recovery: %v", err)
			}
		})
	}
}

func TestFileStoreSequenceWindowExhaustsAtMaximumPartialIV(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	state := sequenceFile{
		Version: fileVersion,
		Next:    maxPartialIV - (sequenceWindowSize - 1),
	}
	if err := writeStateFile(store.dir, store.sequencePath, state); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(store.dir, store.sequenceGuardPath, state); err != nil {
		t.Fatal(err)
	}
	for expected := state.Next; expected <= maxPartialIV; expected++ {
		got, reserveErr := store.Reserve()
		if reserveErr != nil {
			t.Fatalf("reserve sequence %d: %v", expected, reserveErr)
		}
		if got != expected {
			t.Fatalf("reserved sequence = %d, want %d", got, expected)
		}
	}
	if _, err := store.Reserve(); !errors.Is(err, oscore.ErrSenderSequenceExhausted) {
		t.Fatalf("reserve after maximum Partial IV error = %v, want ErrSenderSequenceExhausted", err)
	}
}

func TestFileStoreRejectsInvalidSenderSequenceValues(t *testing.T) {
	for _, next := range []uint64{1, maxPartialIV + 2} {
		t.Run(fmt.Sprintf("next-%d", next), func(t *testing.T) {
			store, err := NewFileStore(t.TempDir(), "invalid-sequence")
			if err != nil {
				t.Fatal(err)
			}
			state := sequenceFile{Version: fileVersion, Next: next}
			if err := writeStateFile(store.dir, store.sequencePath, state); err != nil {
				t.Fatal(err)
			}
			if err := writeStateFile(store.dir, store.sequenceGuardPath, state); err != nil {
				t.Fatal(err)
			}
			if value, err := store.Reserve(); err == nil {
				t.Fatalf("reservation from invalid sender sequence returned %d", value)
			}
		})
	}
}

func TestFileStoreRejectsStaleSenderSequenceTransaction(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "stale-sequence-transaction")
	if err != nil {
		t.Fatal(err)
	}
	current := sequenceFile{Version: fileVersion, Next: 2 * sequenceWindowSize}
	if err := writeStateFile(store.dir, store.sequencePath, current); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(store.dir, store.sequenceGuardPath, current); err != nil {
		t.Fatal(err)
	}
	stale := sequencePendingFile{
		Version:        fileVersion,
		Previous:       sequenceFile{Version: fileVersion},
		PreviousExists: false,
		Next:           sequenceFile{Version: fileVersion, Next: sequenceWindowSize},
	}
	if err := writeStateFile(store.dir, store.sequencePendingPath, stale); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Reserve(); err == nil {
		t.Fatalf("reservation with stale pending transaction returned %d", value)
	}
}

func TestFileStoreSequenceReservationsWithinSingleInstance(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "single-instance-concurrency")
	if err != nil {
		t.Fatal(err)
	}
	const count = 64
	values := make(chan uint64, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, reserveErr := store.Reserve()
			if reserveErr != nil {
				t.Errorf("reserve sequence: %v", reserveErr)
				return
			}
			values <- value
		}()
	}
	wg.Wait()
	close(values)
	seen := make(map[uint64]bool, count)
	for value := range values {
		if seen[value] {
			t.Fatalf("sequence %d was reserved more than once", value)
		}
		seen[value] = true
	}
	if len(seen) != count {
		t.Fatalf("reserved %d values, want %d", len(seen), count)
	}
}

func TestFileStoreRejectsCorruptOrTruncatedState(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *FileStore) error
		path func(*FileStore) string
	}{
		{
			name: "sender sequence",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *FileStore) string { return store.sequencePath },
		},
		{
			name: "sender sequence guard",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *FileStore) string { return store.sequenceGuardPath },
		},
		{
			name: "sender sequence pending transaction",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *FileStore) string { return store.sequencePendingPath },
		},
		{
			name: "replay window",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Load()
				return err
			},
			path: func(store *FileStore) string { return store.replayPath },
		},
		{
			name: "replay rollback guard",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Load()
				return err
			},
			path: func(store *FileStore) string { return store.replayGuardPath },
		},
		{
			name: "replay pending transaction",
			run: func(_ *testing.T, store *FileStore) error {
				_, err := store.Load()
				return err
			},
			path: func(store *FileStore) string { return store.replayPendingPath },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir(), "corrupt-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			path := test.path(store)
			truncated := []byte(`{"version":1,"`)
			err = os.WriteFile(path, truncated, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			err = test.run(t, store)
			if err == nil {
				t.Fatal("corrupt state was accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(truncated) {
				t.Fatalf("corrupt state changed to %q", got)
			}
		})
	}
}

func TestFileStoreReplayPersistenceFailureDoesNotAcceptRequest(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "replay-failure")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("persistent replay failure")
	piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	offlineDir := filepath.Join(t.TempDir(), "offline")
	if err := os.Rename(store.dir, offlineDir); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Open(piv, ciphertext, aad); err == nil {
		t.Fatal("request accepted while durable replay-state storage was unavailable")
	}
	if err := os.Rename(offlineDir, store.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Open(piv, ciphertext, aad); err != nil {
		t.Fatalf("request was consumed despite persistence failure: %v", err)
	}
	if _, err := receiver.Open(piv, ciphertext, aad); !errors.Is(err, oscore.ErrReplay) {
		t.Fatalf("replayed request error = %v, want ErrReplay", err)
	}
}

func TestFileStoreRejectsReplayStateCorruptionDuringRuntime(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "runtime-corruption")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	seal := func(payload string) ([]byte, []byte) {
		t.Helper()
		piv, ciphertext, sealErr := sender.Seal([]byte(payload), func([]byte) ([]byte, error) {
			return []byte("runtime corruption AAD"), nil
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return piv, ciphertext
	}

	aad := []byte("runtime corruption AAD")
	firstPIV, firstCiphertext := seal("first")
	if _, openErr := receiver.Open(firstPIV, firstCiphertext, aad); openErr != nil {
		t.Fatal(openErr)
	}
	validSnapshot, err := os.ReadFile(store.replayPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"version":1,"`)
	if err := os.WriteFile(store.replayPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	secondPIV, secondCiphertext := seal("second")
	if _, err := receiver.Open(secondPIV, secondCiphertext, aad); err == nil {
		t.Fatal("request accepted while replay state was corrupt at runtime")
	}
	if err := os.WriteFile(store.replayPath, validSnapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.Open(secondPIV, secondCiphertext, aad); err != nil {
		t.Fatalf("request was consumed despite runtime corruption: %v", err)
	}
	if _, err := receiver.Open(firstPIV, firstCiphertext, aad); !errors.Is(err, oscore.ErrReplay) {
		t.Fatalf("previously authenticated request error = %v, want ErrReplay", err)
	}
}

func TestFileStoreRecoversInterruptedReplayStateSave(t *testing.T) {
	tests := []replaySaveInterruption{
		{name: "after transaction journal"},
		{name: "after guard update", writeGuard: true},
		{name: "after replay update", writeGuard: true, writeReplay: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runInterruptedReplayStateSave(t, test)
		})
	}
}

type replaySaveInterruption struct {
	name        string
	writeGuard  bool
	writeReplay bool
}

func runInterruptedReplayStateSave(t *testing.T, test replaySaveInterruption) {
	t.Helper()
	dir := t.TempDir()
	store, sender, receiver := newReplaySaveContexts(t, dir)
	aad := []byte("interrupted replay save")
	firstPIV, firstCiphertext := sealReplayTestMessage(t, sender, "first", aad)
	openReplayTestMessage(t, receiver, firstPIV, firstCiphertext, aad)
	previous, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	secondPIV, secondCiphertext := sealReplayTestMessage(t, sender, "second", aad)
	openReplayTestMessage(t, receiver, secondPIV, secondCiphertext, aad)
	next, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	writeInterruptedReplayFiles(t, store, previous, next, test)
	assertInterruptedReplayRecovery(t, dir, store, firstPIV, firstCiphertext, secondPIV, secondCiphertext, aad)
}

func newReplaySaveContexts(t *testing.T, dir string) (*FileStore, *oscore.Context, *oscore.Context) {
	t.Helper()
	store, err := NewFileStore(dir, "interrupted-replay-save")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, sender, receiver
}

func sealReplayTestMessage(t *testing.T, sender *oscore.Context, payload string, aad []byte) ([]byte, []byte) {
	t.Helper()
	piv, ciphertext, err := sender.Seal([]byte(payload), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return piv, ciphertext
}

func openReplayTestMessage(t *testing.T, receiver *oscore.Context, piv, ciphertext, aad []byte) {
	t.Helper()
	if _, err := receiver.Open(piv, ciphertext, aad); err != nil {
		t.Fatal(err)
	}
}

func writeInterruptedReplayFiles(
	t *testing.T,
	store *FileStore,
	previous, next oscore.ReplayWindowState,
	test replaySaveInterruption,
) {
	t.Helper()
	write := func(path string, value any) {
		t.Helper()
		if err := writeStateFile(store.dir, path, value); err != nil {
			t.Fatal(err)
		}
	}
	write(store.replayPath, replayFile{Version: fileVersion, ReplayWindowState: previous})
	write(store.replayGuardPath, replayFile{Version: fileVersion, ReplayWindowState: previous})
	write(store.replayPendingPath, replayPendingFile{Version: fileVersion, Previous: previous, Next: next})
	if test.writeGuard {
		write(store.replayGuardPath, replayFile{Version: fileVersion, ReplayWindowState: next})
	}
	if test.writeReplay {
		write(store.replayPath, replayFile{Version: fileVersion, ReplayWindowState: next})
	}
}

func assertInterruptedReplayRecovery(
	t *testing.T,
	dir string,
	store *FileStore,
	firstPIV, firstCiphertext, secondPIV, secondCiphertext, aad []byte,
) {
	t.Helper()
	restartedStore, err := NewFileStore(dir, "interrupted-replay-save")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: restartedStore,
	})
	if err != nil {
		t.Fatalf("recreate context after interrupted save: %v", err)
	}
	assertReplayRejected(t, restarted, secondPIV, secondCiphertext, aad, "replayed request after recovery")
	assertReplayRejected(t, restarted, firstPIV, firstCiphertext, aad, "earlier accepted request after recovery")
	if _, err := os.Stat(store.replayPendingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending replay transaction remains after recovery: %v", err)
	}
}

func assertReplayRejected(
	t *testing.T,
	context *oscore.Context,
	piv, ciphertext, aad []byte,
	description string,
) {
	t.Helper()
	if _, err := context.Open(piv, ciphertext, aad); !errors.Is(err, oscore.ErrReplay) {
		t.Fatalf("%s error = %v, want ErrReplay", description, err)
	}
}

func TestFileStoreRejectsRestoredReplaySnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "restored-replay-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	newReceiver := func() *oscore.Context {
		t.Helper()
		replayStore, storeErr := NewFileStore(dir, "restored-replay-snapshot")
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		receiver, contextErr := oscore.NewContext(oscore.ContextConfig{
			MasterSecret:      []byte("0123456789abcdef"),
			SenderID:          []byte{1},
			RecipientID:       []byte{0},
			ReplayWindowStore: replayStore,
		})
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		return receiver
	}
	seal := func(payload string) ([]byte, []byte) {
		t.Helper()
		piv, ciphertext, sealErr := sender.Seal([]byte(payload), func([]byte) ([]byte, error) {
			return []byte("rollback AAD"), nil
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return piv, ciphertext
	}
	aad := []byte("rollback AAD")

	firstPIV, firstCiphertext := seal("first")
	if _, openErr := newReceiver().Open(firstPIV, firstCiphertext, aad); openErr != nil {
		t.Fatal(openErr)
	}
	oldSnapshot, err := os.ReadFile(store.replayPath)
	if err != nil {
		t.Fatal(err)
	}
	secondPIV, secondCiphertext := seal("second")
	if _, openErr := newReceiver().Open(secondPIV, secondCiphertext, aad); openErr != nil {
		t.Fatal(openErr)
	}
	if writeErr := os.WriteFile(store.replayPath, oldSnapshot, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	restartedStore, err := NewFileStore(dir, "restored-replay-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: restartedStore,
	})
	if !errors.Is(err, oscore.ErrInvalidReplayState) {
		t.Fatalf("context creation after restoring old replay snapshot error = %v, want ErrInvalidReplayState", err)
	}
	if removeErr := os.Remove(store.replayGuardPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	restartedStore, err = NewFileStore(dir, "restored-replay-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = oscore.NewContext(oscore.ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{0},
		ReplayWindowStore: restartedStore,
	})
	if !errors.Is(err, oscore.ErrInvalidReplayState) {
		t.Fatalf("context creation with missing replay rollback guard error = %v, want ErrInvalidReplayState", err)
	}
}

func TestWriteStateFileRenameFailurePreservesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(dir, target, sequenceFile{Version: fileVersion, Next: 42}); err == nil {
		t.Fatal("expected rename over directory to fail")
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing target content = %q, want existing", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("temporary files remain after failed atomic write: %v", entries)
	}
}

func TestFileStoreReplayStatePersistsAcrossContextRestart(t *testing.T) {
	dir := t.TempDir()
	sender, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	newReceiver := func() *oscore.Context {
		t.Helper()
		store, storeErr := NewFileStore(dir, "receiver-context")
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		context, contextErr := oscore.NewContext(oscore.ContextConfig{
			MasterSecret:      []byte("0123456789abcdef"),
			SenderID:          []byte{1},
			RecipientID:       []byte{0},
			ReplayWindowStore: store,
		})
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		return context
	}

	aad := []byte("OSCORE test AAD")
	piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newReceiver().Open(piv, ciphertext, aad); err != nil {
		t.Fatal(err)
	}
	if _, err := newReceiver().Open(piv, ciphertext, aad); !errors.Is(err, oscore.ErrReplay) {
		t.Fatalf("replayed message after restart error = %v, want ErrReplay", err)
	}
}

func TestFileStoreRejectsConflictingReplayState(t *testing.T) {
	first, err := NewFileStore(t.TempDir(), "shared-receiver")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileStore(first.dir, "shared-receiver")
	if err != nil {
		t.Fatal(err)
	}
	client, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte("0123456789abcdef"),
		SenderID:     []byte{0},
		RecipientID:  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	newReceiver := func(store *FileStore) *oscore.Context {
		context, contextErr := oscore.NewContext(oscore.ContextConfig{
			MasterSecret:      []byte("0123456789abcdef"),
			SenderID:          []byte{1},
			RecipientID:       []byte{0},
			ReplayWindowStore: store,
		})
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		return context
	}
	receivers := []*oscore.Context{newReceiver(first), newReceiver(second)}
	aad := []byte("OSCORE test AAD")
	piv, ciphertext, err := client.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(receivers))
	for _, receiver := range receivers {
		wg.Add(1)
		go func(receiver *oscore.Context) {
			defer wg.Done()
			_, err := receiver.Open(piv, ciphertext, aad)
			results <- err
		}(receiver)
	}
	wg.Wait()
	close(results)

	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, oscore.ErrReplay):
			rejected++
		default:
			t.Fatalf("unexpected open error: %v", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted %d copies and rejected %d, want one of each", accepted, rejected)
	}
}
