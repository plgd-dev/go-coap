package store

import (
	"errors"
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
