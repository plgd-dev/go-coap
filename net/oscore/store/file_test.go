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
	next, err := first.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if next != count {
		t.Fatalf("next sequence = %d, want %d", next, count)
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
