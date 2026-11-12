package store

import (
	"errors"
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

func TestFileStoreSequenceWindowExhaustsAtMaximumPartialIV(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(store.dir, store.sequencePath, sequenceFile{
		Version: fileVersion,
		Next:    maxPartialIV - 1,
	}); err != nil {
		t.Fatal(err)
	}
	for expected := maxPartialIV - 1; expected <= maxPartialIV; expected++ {
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
	tests := []struct {
		name        string
		writeGuard  bool
		writeReplay bool
	}{
		{name: "after transaction journal"},
		{name: "after guard update", writeGuard: true},
		{name: "after replay update", writeGuard: true, writeReplay: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
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
			aad := []byte("interrupted replay save")
			seal := func(payload string) ([]byte, []byte) {
				t.Helper()
				piv, ciphertext, sealErr := sender.Seal([]byte(payload), func([]byte) ([]byte, error) {
					return aad, nil
				})
				if sealErr != nil {
					t.Fatal(sealErr)
				}
				return piv, ciphertext
			}

			firstPIV, firstCiphertext := seal("first")
			if _, openErr := receiver.Open(firstPIV, firstCiphertext, aad); openErr != nil {
				t.Fatal(openErr)
			}
			previous, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			secondPIV, secondCiphertext := seal("second")
			if _, openErr := receiver.Open(secondPIV, secondCiphertext, aad); openErr != nil {
				t.Fatal(openErr)
			}
			next, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if writeErr := writeStateFile(dir, store.replayPath, replayFile{
				Version: fileVersion, ReplayWindowState: previous,
			}); writeErr != nil {
				t.Fatal(writeErr)
			}
			if writeErr := writeStateFile(dir, store.replayGuardPath, replayFile{
				Version: fileVersion, ReplayWindowState: previous,
			}); writeErr != nil {
				t.Fatal(writeErr)
			}
			pending := replayPendingFile{
				Version:  fileVersion,
				Previous: previous,
				Next:     next,
			}
			if writeErr := writeStateFile(dir, store.replayPendingPath, pending); writeErr != nil {
				t.Fatal(writeErr)
			}
			if test.writeGuard {
				if writeErr := writeStateFile(dir, store.replayGuardPath, replayFile{
					Version: fileVersion, ReplayWindowState: next,
				}); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			if test.writeReplay {
				if writeErr := writeStateFile(dir, store.replayPath, replayFile{
					Version: fileVersion, ReplayWindowState: next,
				}); writeErr != nil {
					t.Fatal(writeErr)
				}
			}

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
			if _, err := restarted.Open(secondPIV, secondCiphertext, aad); !errors.Is(err, oscore.ErrReplay) {
				t.Fatalf("replayed request after recovery error = %v, want ErrReplay", err)
			}
			if _, err := restarted.Open(firstPIV, firstCiphertext, aad); !errors.Is(err, oscore.ErrReplay) {
				t.Fatalf("earlier accepted request after recovery error = %v, want ErrReplay", err)
			}
			if _, err := os.Stat(store.replayPendingPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending replay transaction remains after recovery: %v", err)
			}
		})
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
