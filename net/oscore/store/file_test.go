package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

func TestHighWaterFileStoreSequenceReservationsAcrossInstances(t *testing.T) {
	first, err := NewHighWaterFileStore(t.TempDir(), "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewHighWaterFileStore(first.dir, "peer-a")
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
	restarted, err := NewHighWaterFileStore(first.dir, "peer-a")
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

func TestHighWaterFileStoreSequenceWindowSkipsUnusedValuesAfterRestart(t *testing.T) {
	first, err := NewHighWaterFileStore(t.TempDir(), "peer-a")
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

	restarted, err := NewHighWaterFileStore(first.dir, "peer-a")
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

func TestHighWaterFileStoreRejectsMissingSenderSequenceStateOrGuard(t *testing.T) {
	for _, test := range []struct {
		name string
		path func(*HighWaterFileStore) string
	}{
		{name: "sequence file", path: func(s *HighWaterFileStore) string { return s.sequencePath }},
		{name: "sequence guard", path: func(s *HighWaterFileStore) string { return s.sequenceGuardPath }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewHighWaterFileStore(t.TempDir(), "missing-"+test.name)
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
			restarted, err := NewHighWaterFileStore(store.dir, "missing-"+test.name)
			if err != nil {
				t.Fatal(err)
			}
			if value, err := restarted.Reserve(); err == nil {
				t.Fatalf("reservation after deleting %s returned %d", test.name, value)
			}
		})
	}
}

func TestHighWaterFileStoreRejectsMissingSenderStateAndGuardAfterWindow(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "missing-sequence-and-guard")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if first != 0 {
		t.Fatalf("first sequence = %d, want 0", first)
	}
	if err := os.Remove(store.sequencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.sequenceGuardPath); err != nil {
		t.Fatal(err)
	}

	for expected := uint64(1); expected < sequenceWindowSize; expected++ {
		value, reserveErr := store.Reserve()
		if reserveErr != nil {
			t.Fatalf("reserve remaining in-memory sequence %d: %v", expected, reserveErr)
		}
		if value != expected {
			t.Fatalf("remaining in-memory sequence = %d, want %d", value, expected)
		}
	}
	if value, err := store.Reserve(); err == nil {
		t.Fatalf("reservation after in-memory window was exhausted returned %d", value)
	}
}

func TestHighWaterFileStoreRemembersLoadedStateAfterPendingRemovalSyncFailure(t *testing.T) {
	dir := t.TempDir()
	first, err := NewHighWaterFileStore(dir, "pending-removal-sync-failure")
	if err != nil {
		t.Fatal(err)
	}
	if value, reserveErr := first.Reserve(); reserveErr != nil || value != 0 {
		t.Fatalf("first reservation = %d, %v; want 0", value, reserveErr)
	}
	second, err := NewHighWaterFileStore(dir, "pending-removal-sync-failure")
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("injected pending removal directory sync failure")
	pendingSyncs := 0
	second.sequenceOps.syncDirectory = func(path, syncDir string) error {
		if path == second.sequencePendingPath {
			pendingSyncs++
			if pendingSyncs == 2 {
				return syncErr
			}
		}
		return syncDirectory(syncDir)
	}
	if value, err := second.Reserve(); !errors.Is(err, syncErr) {
		t.Fatalf("reservation = %d, %v; want pending removal sync failure", value, err)
	}
	if pendingSyncs != 2 {
		t.Fatalf("pending transaction directory syncs = %d, want 2", pendingSyncs)
	}
	if err := os.Remove(second.sequencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second.sequenceGuardPath); err != nil {
		t.Fatal(err)
	}
	if value, err := second.Reserve(); err == nil {
		t.Fatalf("reservation after losing state files returned %d", value)
	}
}

func TestHighWaterFileStoreSequencePersistenceFailureDoesNotReturnAValue(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHighWaterFileStore(dir, "sender-sequence-storage-failure")
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

func TestHighWaterFileStoreSequencePersistenceFaultInjection(t *testing.T) {
	tests := []struct {
		name         string
		point        sequencePersistenceFaultPoint
		retryInPlace bool
	}{
		{name: "pending transaction write", point: sequencePendingWrite, retryInPlace: true},
		{name: "sequence guard write", point: sequenceGuardWrite},
		{name: "sequence state write", point: sequenceStateWrite},
		{name: "pending directory sync", point: sequencePendingSync},
		{name: "guard directory sync", point: sequenceGuardSync},
		{name: "sequence directory sync", point: sequenceStateSync},
		{name: "pending removal", point: sequencePendingRemove},
		{name: "pending removal directory sync", point: sequencePendingRemoveSync},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runSequencePersistenceFaultCase(t, test.name, test.point, test.retryInPlace)
		})
	}
}

type sequencePersistenceFaultPoint uint8

const (
	sequencePendingWrite sequencePersistenceFaultPoint = iota
	sequenceGuardWrite
	sequenceStateWrite
	sequencePendingSync
	sequenceGuardSync
	sequenceStateSync
	sequencePendingRemove
	sequencePendingRemoveSync
)

func runSequencePersistenceFaultCase(
	t *testing.T,
	name string,
	point sequencePersistenceFaultPoint,
	retryInPlace bool,
) {
	t.Helper()
	dir := t.TempDir()
	id := "sequence-fault-" + name
	store, err := NewHighWaterFileStore(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	injectedFailure := errors.New("injected sender sequence persistence failure")
	injectSequencePersistenceFault(store, point, injectedFailure)
	if value, reserveErr := store.Reserve(); !errors.Is(reserveErr, injectedFailure) {
		t.Fatalf("reservation = %d, %v; want injected failure", value, reserveErr)
	}
	if retryInPlace {
		assertSequenceRetryInPlace(t, store)
		return
	}
	assertSequenceRetryAfterRecovery(t, dir, id)
}

func injectSequencePersistenceFault(store *HighWaterFileStore, point sequencePersistenceFaultPoint, failure error) {
	switch point {
	case sequencePendingWrite:
		injectSequenceWriteFailure(store, store.sequencePendingPath, failure)
	case sequenceGuardWrite:
		injectSequenceWriteFailure(store, store.sequenceGuardPath, failure)
	case sequenceStateWrite:
		injectSequenceWriteFailure(store, store.sequencePath, failure)
	case sequencePendingSync:
		injectSequenceSyncFailure(store, store.sequencePendingPath, failure)
	case sequenceGuardSync:
		injectSequenceSyncFailure(store, store.sequenceGuardPath, failure)
	case sequenceStateSync:
		injectSequenceSyncFailure(store, store.sequencePath, failure)
	case sequencePendingRemove:
		store.sequenceOps.beforeRemove = func(string) error { return failure }
	case sequencePendingRemoveSync:
		injectPendingRemovalSyncFailure(store, failure)
	}
}

func injectSequenceWriteFailure(store *HighWaterFileStore, path string, failure error) {
	store.sequenceOps.beforeWrite = func(writePath string) error {
		if writePath == path {
			return failure
		}
		return nil
	}
}

func injectSequenceSyncFailure(store *HighWaterFileStore, path string, failure error) {
	store.sequenceOps.syncDirectory = func(syncPath, dir string) error {
		if syncPath == path {
			return failure
		}
		return syncDirectory(dir)
	}
}

func injectPendingRemovalSyncFailure(store *HighWaterFileStore, failure error) {
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
}

func assertSequenceRetryInPlace(t *testing.T, store *HighWaterFileStore) {
	t.Helper()
	store.sequenceOps = sequenceFileOps{}
	if value, err := store.Reserve(); err != nil || value != 0 {
		t.Fatalf("reservation after pre-transaction failure = %d, %v; want 0", value, err)
	}
}

func assertSequenceRetryAfterRecovery(t *testing.T, dir, id string) {
	t.Helper()
	restarted, err := NewHighWaterFileStore(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := restarted.Reserve(); err != nil || value != sequenceWindowSize {
		t.Fatalf("reservation after interrupted transaction = %d, %v; want %d", value, err, sequenceWindowSize)
	}
}

func TestHighWaterFileStoreRejectsRestoredSenderSequenceSnapshot(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHighWaterFileStore(dir, "restored-sender-sequence")
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
	restarted, err := NewHighWaterFileStore(dir, "restored-sender-sequence")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := restarted.Reserve(); err == nil {
		t.Fatalf("reservation after restoring old sequence snapshot returned %d", value)
	}
}

func TestHighWaterFileStoreRecoversInterruptedSequenceReservation(t *testing.T) {
	for _, stage := range interruptedSequenceReservationStages() {
		t.Run(stage.name, func(t *testing.T) {
			runInterruptedSequenceReservation(t, stage)
		})
	}
}

type sequenceReservationInterruption struct {
	name          string
	initialized   bool
	writeGuard    bool
	writeSequence bool
}

func interruptedSequenceReservationStages() []sequenceReservationInterruption {
	return []sequenceReservationInterruption{
		{name: "after pending transaction"},
		{name: "after guard", writeGuard: true},
		{name: "after sequence", writeGuard: true, writeSequence: true},
		{name: "existing state after pending", initialized: true},
		{name: "existing state after guard", initialized: true, writeGuard: true},
		{name: "existing state after sequence", initialized: true, writeGuard: true, writeSequence: true},
	}
}

func runInterruptedSequenceReservation(t *testing.T, stage sequenceReservationInterruption) {
	t.Helper()
	dir := t.TempDir()
	id := "interrupted-sequence-" + stage.name
	store, err := NewHighWaterFileStore(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	previous := sequenceFile{Version: fileVersion}
	if stage.initialized {
		previous.Next = sequenceWindowSize
		writeSequenceFile(t, dir, store.sequencePath, previous)
		writeSequenceFile(t, dir, store.sequenceGuardPath, previous)
	}
	next := sequenceFile{Version: fileVersion, Next: previous.Next + sequenceWindowSize}
	pending := sequencePendingFile{
		Version:        fileVersion,
		Previous:       previous,
		PreviousExists: stage.initialized,
		Next:           next,
	}
	writeSequenceFile(t, dir, store.sequencePendingPath, pending)
	if stage.writeGuard {
		writeSequenceFile(t, dir, store.sequenceGuardPath, next)
	}
	if stage.writeSequence {
		writeSequenceFile(t, dir, store.sequencePath, next)
	}
	assertInterruptedSequenceRecovery(t, store, dir, id, next.Next)
}

func writeSequenceFile(t *testing.T, dir, path string, state any) {
	t.Helper()
	if err := writeStateFile(dir, path, state); err != nil {
		t.Fatal(err)
	}
}

func assertInterruptedSequenceRecovery(t *testing.T, store *HighWaterFileStore, dir, id string, want uint64) {
	t.Helper()
	restarted, err := NewHighWaterFileStore(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	value, err := restarted.Reserve()
	if err != nil {
		t.Fatalf("reserve after recovering interrupted transaction: %v", err)
	}
	if value != want {
		t.Fatalf("reservation after recovery = %d, want %d", value, want)
	}
	if _, err := os.Stat(store.sequencePendingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending sequence transaction remains after recovery: %v", err)
	}
}

func TestHighWaterFileStoreSequenceWindowExhaustsAtMaximumPartialIV(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "peer-a")
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

func TestHighWaterFileStoreRejectsInvalidSenderSequenceValues(t *testing.T) {
	for _, next := range []uint64{1, maxPartialIV + 2} {
		t.Run(fmt.Sprintf("next-%d", next), func(t *testing.T) {
			store, err := NewHighWaterFileStore(t.TempDir(), "invalid-sequence")
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

func TestHighWaterFileStoreRejectsStaleSenderSequenceTransaction(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "stale-sequence-transaction")
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

func TestHighWaterFileStoreSequenceReservationsWithinSingleInstance(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "single-instance-concurrency")
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

func TestHighWaterFileStoreRejectsCorruptOrTruncatedState(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *HighWaterFileStore) error
		path func(*HighWaterFileStore) string
	}{
		{
			name: "sender sequence",
			run: func(_ *testing.T, store *HighWaterFileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *HighWaterFileStore) string { return store.sequencePath },
		},
		{
			name: "sender sequence guard",
			run: func(_ *testing.T, store *HighWaterFileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *HighWaterFileStore) string { return store.sequenceGuardPath },
		},
		{
			name: "sender sequence pending transaction",
			run: func(_ *testing.T, store *HighWaterFileStore) error {
				_, err := store.Reserve()
				return err
			},
			path: func(store *HighWaterFileStore) string { return store.sequencePendingPath },
		},
		{
			name: "replay high-water mark",
			run: func(_ *testing.T, store *HighWaterFileStore) error {
				_, _, err := store.LoadHighWater()
				return err
			},
			path: func(store *HighWaterFileStore) string { return store.replayHighWaterPath },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewHighWaterFileStore(t.TempDir(), "corrupt-"+test.name)
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

func TestHighWaterFileStoreReplayCutoffSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHighWaterFileStore(dir, "high-water-replay")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            store,
	})
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("high-water replay")
	var firstPIV, boundaryPIV []byte
	var firstCiphertext, boundaryCiphertext []byte
	for sequence := range uint64(257) {
		piv, ciphertext, sealErr := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
			return aad, nil
		})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if sequence == 0 {
			firstPIV, firstCiphertext = piv, ciphertext
		}
		if sequence == 256 {
			boundaryPIV, boundaryCiphertext = piv, ciphertext
		}
		if _, openErr := receiver.Open(piv, ciphertext, aad); openErr != nil {
			t.Fatalf("open sequence %d: %v", sequence, openErr)
		}
	}
	highWater, exists, err := store.LoadHighWater()
	if err != nil || !exists {
		t.Fatalf("load high-water mark = %d, %t, %v", highWater, exists, err)
	}
	if highWater != 512 {
		t.Fatalf("high-water mark = %d, want 512", highWater)
	}

	restartedStore, err := NewHighWaterFileStore(dir, "high-water-replay")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            restartedStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, replay := range []struct {
		name       string
		piv        []byte
		ciphertext []byte
	}{
		{name: "oldest accepted", piv: firstPIV, ciphertext: firstCiphertext},
		{name: "last accepted below cutoff", piv: boundaryPIV, ciphertext: boundaryCiphertext},
	} {
		t.Run(replay.name, func(t *testing.T) {
			if _, err := restarted.Open(replay.piv, replay.ciphertext, aad); !errors.Is(err, oscore.ErrReplay) {
				t.Fatalf("replayed request error = %v, want ErrReplay", err)
			}
		})
	}
}

func TestHighWaterFileStoreConfigurableReplayMargin(t *testing.T) {
	for _, margin := range []uint64{1, 16, 256} {
		t.Run(fmt.Sprintf("margin-%d", margin), func(t *testing.T) {
			testHighWaterFileStoreReplayMargin(t, margin)
		})
	}
}

func testHighWaterFileStoreReplayMargin(t *testing.T, margin uint64) {
	t.Helper()
	const storeID = "high-water-margin"
	dir := t.TempDir()
	store, err := NewHighWaterFileStore(dir, storeID, WithReplayHighWaterMargin(margin))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            store,
	})
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("configurable replay margin")
	firstPIV, firstCiphertext := acceptReplayMarginSequences(t, sender, receiver, aad, margin)
	highWater, exists, err := store.LoadHighWater()
	if err != nil || !exists {
		t.Fatalf("LoadHighWater = %d, %t, %v", highWater, exists, err)
	}
	if highWater != 2*margin {
		t.Fatalf("high-water mark = %d, want %d", highWater, 2*margin)
	}
	assertReplayMarginRestartRejects(t, dir, storeID, margin, firstPIV, firstCiphertext, aad)
}

func acceptReplayMarginSequences(
	t *testing.T, sender, receiver *oscore.Context, aad []byte, margin uint64,
) ([]byte, []byte) {
	t.Helper()
	var firstPIV, firstCiphertext []byte
	for sequence := range margin + 1 {
		piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
			return aad, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if sequence == 0 {
			firstPIV, firstCiphertext = piv, ciphertext
		}
		if _, err := receiver.Open(piv, ciphertext, aad); err != nil {
			t.Fatalf("open sequence %d: %v", sequence, err)
		}
	}
	return firstPIV, firstCiphertext
}

func assertReplayMarginRestartRejects(
	t *testing.T, dir, storeID string, margin uint64, piv, ciphertext, aad []byte,
) {
	t.Helper()
	restartedStore, err := NewHighWaterFileStore(dir, storeID, WithReplayHighWaterMargin(margin))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            restartedStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, replayErr := restarted.Open(piv, ciphertext, aad); !errors.Is(replayErr, oscore.ErrReplay) {
		t.Fatalf("replayed sequence after restart = %v, want ErrReplay", replayErr)
	}
}

func TestHighWaterFileStoreRejectsInvalidReplayMargins(t *testing.T) {
	for _, margin := range []uint64{0, 1025} {
		if _, err := NewHighWaterFileStore(
			t.TempDir(), fmt.Sprintf("invalid-margin-%d", margin), WithReplayHighWaterMargin(margin),
		); !errors.Is(err, ErrInvalidReplayHighWaterMargin) {
			t.Fatalf("margin %d error = %v, want ErrInvalidReplayHighWaterMargin", margin, err)
		}
	}
}

func TestHighWaterFileStoreReplayMarkAndGuardDeletion(t *testing.T) {
	for _, test := range []struct {
		name        string
		deleteFiles func(*HighWaterFileStore) []string
		wantError   bool
	}{
		{
			name: "fresh directory",
		},
		{
			name: "mark deleted",
			deleteFiles: func(store *HighWaterFileStore) []string {
				return []string{store.replayHighWaterPath}
			},
			wantError: true,
		},
		{
			name: "guard deleted",
			deleteFiles: func(store *HighWaterFileStore) []string {
				return []string{store.replayGuardPath}
			},
			wantError: true,
		},
		{
			name: "both deleted",
			deleteFiles: func(store *HighWaterFileStore) []string {
				return []string{store.replayHighWaterPath, store.replayGuardPath}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			testHighWaterFileStoreReplayDeletion(t, test)
		})
	}
}

func testHighWaterFileStoreReplayDeletion(
	t *testing.T,
	test struct {
		name        string
		deleteFiles func(*HighWaterFileStore) []string
		wantError   bool
	},
) {
	t.Helper()
	store, err := NewHighWaterFileStore(t.TempDir(), "replay-deletion-"+test.name)
	if err != nil {
		t.Fatal(err)
	}
	deleteReplayFiles(t, store, test.deleteFiles)
	highWater, exists, err := store.LoadHighWater()
	assertReplayDeletionResult(t, highWater, exists, err, test.wantError)
}

func deleteReplayFiles(t *testing.T, store *HighWaterFileStore, selectFiles func(*HighWaterFileStore) []string) {
	t.Helper()
	if selectFiles == nil {
		return
	}
	if err := store.SaveHighWater(256); err != nil {
		t.Fatal(err)
	}
	for _, path := range selectFiles(store) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func assertReplayDeletionResult(t *testing.T, highWater uint64, exists bool, err error, wantError bool) {
	t.Helper()
	if wantError {
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("LoadHighWater error = %v, want missing-state error", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("LoadHighWater: %v", err)
	}
	if exists || highWater != 0 {
		t.Fatalf("LoadHighWater = %d, %t, want fresh state", highWater, exists)
	}
}

func TestHighWaterFileStoreResetReplayState(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "reset-replay-state")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveHighWater(256); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetReplayState(); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.LoadHighWater(); err != nil || exists {
		t.Fatalf("LoadHighWater after reset = exists %t, err %v; want empty state", exists, err)
	}
}

func TestHighWaterFileStoreResetRetrySyncsDirectory(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "reset-replay-sync-retry")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHighWater(256); err != nil {
		t.Fatal(err)
	}

	syncErr := errors.New("simulated reset directory sync failure")
	var syncAttempts int
	store.replayOps.syncDirectory = func(path, dir string) error {
		if path == store.replayHighWaterPath {
			syncAttempts++
			if syncAttempts == 1 {
				return syncErr
			}
		}
		return syncDirectory(dir)
	}
	if err := store.ResetReplayState(); !errors.Is(err, syncErr) {
		t.Fatalf("first ResetReplayState error = %v, want %v", err, syncErr)
	}
	if err := store.ResetReplayState(); err != nil {
		t.Fatalf("retry ResetReplayState: %v", err)
	}
	if syncAttempts != 2 {
		t.Fatalf("directory sync attempts = %d, want 2", syncAttempts)
	}
	if _, exists, err := store.LoadHighWater(); err != nil || exists {
		t.Fatalf("LoadHighWater after reset retry = exists %t, err %v; want empty state", exists, err)
	}
}

func TestHighWaterFileStoreRecoversInterruptedReplayPairUpdate(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "replay-pair-recovery")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("replay pair recovery")
	piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveHighWater(256); err != nil {
		t.Fatal(err)
	}
	markWriteErr := errors.New("simulated replay mark write failure")
	store.replayOps.beforeWrite = func(path string) error {
		if path == store.replayHighWaterPath {
			store.replayOps.beforeWrite = nil
			return markWriteErr
		}
		return nil
	}
	if err = store.SaveHighWater(512); !errors.Is(err, markWriteErr) {
		t.Fatalf("SaveHighWater error = %v, want %v", err, markWriteErr)
	}

	highWater, exists, err := store.LoadHighWater()
	if err != nil {
		t.Fatalf("LoadHighWater should recover interrupted pair update: %v", err)
	}
	if !exists || highWater != 512 {
		t.Fatalf("LoadHighWater = %d, %t; want 512, true", highWater, exists)
	}
	_, err = os.Stat(store.replayPendingPath)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay pending transaction still exists after recovery: %v", err)
	}

	restartedStore, err := NewHighWaterFileStore(store.dir, "replay-pair-recovery")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            restartedStore,
	})
	if err != nil {
		t.Fatalf("recreate context after transaction recovery: %v", err)
	}
	if _, err := restarted.Open(piv, ciphertext, aad); !errors.Is(err, oscore.ErrReplay) {
		t.Fatalf("replay after transaction recovery = %v, want ErrReplay", err)
	}
	if err := restartedStore.SaveHighWater(768); err != nil {
		t.Fatalf("save next cutoff after recovery: %v", err)
	}
}

func TestHighWaterFileStoreRestartAfterDirectorySyncFailureRejectsAcceptedReplay(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "high-water-sync-failure")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            store,
	})
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("high-water crash")
	piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
		return aad, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("simulated crash after atomic rename")
	store.replayOps.syncDirectory = func(path, dir string) error {
		if path == store.replayHighWaterPath {
			store.replayOps.syncDirectory = nil
			return syncErr
		}
		return syncDirectory(dir)
	}
	if _, openErr := receiver.Open(piv, ciphertext, aad); !errors.Is(openErr, syncErr) {
		t.Fatalf("Open error = %v, want simulated sync failure", openErr)
	}

	restartedStore, err := NewHighWaterFileStore(store.dir, "high-water-sync-failure")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{1},
		RecipientID:                  []byte{0},
		ReplayWindowStore:            restartedStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, openErr := restarted.Open(piv, ciphertext, aad); !errors.Is(openErr, oscore.ErrReplay) {
		t.Fatalf("request after restart error = %v, want ErrReplay", openErr)
	}
}

func TestHighWaterFileStoreRecoverySyncsBeforeRemovingPendingJournal(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "replay-recovery-sync-before-remove")
	if err != nil {
		t.Fatal(err)
	}
	initialSyncErr := errors.New("simulated cutoff directory sync failure")
	store.replayOps.syncDirectory = func(path, dir string) error {
		if path == store.replayHighWaterPath {
			store.replayOps.syncDirectory = nil
			return initialSyncErr
		}
		return syncDirectory(dir)
	}
	saveErr := store.SaveHighWater(256)
	if !errors.Is(saveErr, initialSyncErr) {
		t.Fatalf("SaveHighWater error = %v, want %v", saveErr, initialSyncErr)
	}

	recoverySyncErr := errors.New("simulated recovery directory sync failure")
	store.replayOps.syncDirectory = func(_, _ string) error {
		return recoverySyncErr
	}
	_, _, loadErr := store.LoadHighWater()
	if !errors.Is(loadErr, recoverySyncErr) {
		t.Fatalf("LoadHighWater error = %v, want %v", loadErr, recoverySyncErr)
	}
	_, statErr := os.Stat(store.replayPendingPath)
	if statErr != nil {
		t.Fatalf("pending transaction removed before recovered state was synced: %v", statErr)
	}

	store.replayOps.syncDirectory = nil
	highWater, exists, err := store.LoadHighWater()
	if err != nil {
		t.Fatalf("retry LoadHighWater after recovery sync failure: %v", err)
	}
	if !exists || highWater != 256 {
		t.Fatalf("recovered high-water mark = %d, %t; want 256, true", highWater, exists)
	}
	if _, err := os.Stat(store.replayPendingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending transaction remains after successful recovery: %v", err)
	}
}

func TestHighWaterFileStoreEqualMarkRetrySyncsDirectory(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "high-water-equal-mark-sync")
	if err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("simulated directory sync failure")
	var syncAttempts int
	store.replayOps.syncDirectory = func(path, dir string) error {
		if path == store.replayHighWaterPath {
			syncAttempts++
			if syncAttempts == 1 {
				return syncErr
			}
		}
		return syncDirectory(dir)
	}
	if err := store.SaveHighWater(256); !errors.Is(err, syncErr) {
		t.Fatalf("first SaveHighWater error = %v, want %v", err, syncErr)
	}
	if err := store.SaveHighWater(256); err != nil {
		t.Fatalf("retry SaveHighWater with equal mark: %v", err)
	}
	if syncAttempts != 3 {
		t.Fatalf("directory sync attempts = %d, want 3 including recovery and equal-mark retry", syncAttempts)
	}

	store.replayOps.syncDirectory = func(path, dir string) error {
		if path == store.replayHighWaterPath {
			return syncErr
		}
		return syncDirectory(dir)
	}
	if err := store.SaveHighWater(256); !errors.Is(err, syncErr) {
		t.Fatalf("equal-mark SaveHighWater with sync failure = %v, want %v", err, syncErr)
	}
}

func BenchmarkHighWaterFileStoreReplayPersistence(b *testing.B) {
	for _, margin := range []uint64{1, 16, 256} {
		b.Run(fmt.Sprintf("margin-%d", margin), func(b *testing.B) {
			store, err := NewHighWaterFileStore(
				b.TempDir(), "benchmark", WithReplayHighWaterMargin(margin),
			)
			if err != nil {
				b.Fatal(err)
			}
			var syncCount uint64
			store.replayOps.syncFile = func(_ string, file *os.File) error {
				syncCount++
				return file.Sync()
			}
			store.replayOps.syncDirectory = func(_, dir string) error {
				syncCount++
				return syncDirectory(dir)
			}
			sender, err := oscore.NewContext(oscore.ContextConfig{
				AcceptVolatileSenderSequence: true,
				AcceptReplayStateLoss:        true,
				MasterSecret:                 []byte("0123456789abcdef"),
				SenderID:                     []byte{0},
				RecipientID:                  []byte{1},
			})
			if err != nil {
				b.Fatal(err)
			}
			receiver, err := oscore.NewContext(oscore.ContextConfig{
				AcceptVolatileSenderSequence: true,
				MasterSecret:                 []byte("0123456789abcdef"),
				SenderID:                     []byte{1},
				RecipientID:                  []byte{0},
				ReplayWindowStore:            store,
			})
			if err != nil {
				b.Fatal(err)
			}
			aad := []byte("benchmark")
			b.ResetTimer()
			for range b.N {
				piv, ciphertext, err := sender.Seal([]byte("request"), func([]byte) ([]byte, error) {
					return aad, nil
				})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := receiver.Open(piv, ciphertext, aad); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(syncCount)/float64(b.N), "fsyncs/op")
		})
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

func TestHighWaterFileStoreReplayStatePersistsAcrossContextRestart(t *testing.T) {
	dir := t.TempDir()
	sender, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("0123456789abcdef"),
		SenderID:                     []byte{0},
		RecipientID:                  []byte{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	newReceiver := func() *oscore.Context {
		t.Helper()
		store, storeErr := NewHighWaterFileStore(dir, "receiver-context")
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		context, contextErr := oscore.NewContext(oscore.ContextConfig{
			AcceptVolatileSenderSequence: true,
			MasterSecret:                 []byte("0123456789abcdef"),
			SenderID:                     []byte{1},
			RecipientID:                  []byte{0},
			ReplayWindowStore:            store,
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
