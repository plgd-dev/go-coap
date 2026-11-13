package oscore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestReplayWindowSlidingAndAuthentication(t *testing.T) {
	window, err := NewReplayWindow(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := window.Accept(10, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	authErr := errors.New("authentication failed")
	if err := window.Accept(12, func() error { return authErr }); !errors.Is(err, authErr) {
		t.Fatalf("failed authentication error = %v", err)
	}
	if err := window.Accept(12, func() error { return nil }); err != nil {
		t.Fatalf("failed authentication consumed sequence: %v", err)
	}
	if err := window.Accept(10, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("duplicate sequence error = %v", err)
	}
	if err := window.Accept(9, func() error { return nil }); err != nil {
		t.Fatalf("in-window out-of-order sequence error = %v", err)
	}
	if err := window.Accept(8, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("stale sequence error = %v", err)
	}
}

func TestReplayWindowConcurrentDuplicates(t *testing.T) {
	window, err := NewReplayWindow(32)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := window.Accept(7, func() error {
				accepted.Add(1)
				return nil
			})
			if err != nil && !errors.Is(err, ErrReplay) {
				t.Errorf("Accept returned unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("authenticated %d copies, want exactly one", got)
	}
}

type testReplayWindowStore struct {
	state             ReplayWindowState
	loadErr           error
	saveErr           error
	commitOnSaveError bool
}

func (s *testReplayWindowStore) Load() (ReplayWindowState, error) {
	return s.state, s.loadErr
}

func (s *testReplayWindowStore) Save(state ReplayWindowState) error {
	if s.saveErr != nil {
		if s.commitOnSaveError {
			s.state = state
			s.commitOnSaveError = false
		}
		return s.saveErr
	}
	s.state = state
	return nil
}

type testReplayWindowHighWaterStore struct {
	testReplayWindowStore
	highWater        uint64
	highWaterSet     bool
	loadHighWaterErr error
	saveHighWaterErr error
	saveAttempts     int
	commitOnError    bool
}

func (s *testReplayWindowHighWaterStore) LoadHighWater() (uint64, bool, error) {
	if s.loadHighWaterErr != nil {
		return 0, false, s.loadHighWaterErr
	}
	return s.highWater, s.highWaterSet, nil
}

func (s *testReplayWindowHighWaterStore) SaveHighWater(highWater uint64) error {
	s.saveAttempts++
	if s.saveHighWaterErr != nil {
		if s.commitOnError {
			s.highWater = highWater
			s.highWaterSet = true
			s.commitOnError = false
		}
		return s.saveHighWaterErr
	}
	s.highWater = highWater
	s.highWaterSet = true
	return nil
}

func (s *testReplayWindowHighWaterStore) restart(t *testing.T, size uint8) *ReplayWindow {
	t.Helper()
	state, highWaterSet, err := loadReplayState(s, size)
	if err != nil {
		t.Fatalf("load replay state after restart: %v", err)
	}
	window, err := newReplayWindow(size, state, s, highWaterSet)
	if err != nil {
		t.Fatalf("create replay window after restart: %v", err)
	}
	return window
}

func TestReplayWindowHighWaterSaveFailureReconcilesPersistedMark(t *testing.T) {
	saveErr := errors.New("high-water write reported failure")
	store := &testReplayWindowHighWaterStore{}
	window, err := newReplayWindow(4, ReplayWindowState{}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if acceptErr := window.Accept(0, func() error { return nil }); acceptErr != nil {
		t.Fatal(acceptErr)
	}
	store.saveHighWaterErr = saveErr
	store.commitOnError = true
	if err := window.Accept(300, func() error { return nil }); !errors.Is(err, saveErr) {
		t.Fatalf("persistence error = %v, want %v", err, saveErr)
	}
	if !store.highWaterSet {
		t.Fatal("test store did not commit the high-water mark")
	}

	restarted := store.restart(t, 4)
	if err := restarted.Accept(0, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed previously accepted sequence after restart error = %v, want ErrReplay", err)
	}
	if err := restarted.Accept(300, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed sequence covered by persisted mark error = %v, want ErrReplay", err)
	}
}

func TestReplayWindowHighWaterSaveAndReconcileFailureRetryPersistence(t *testing.T) {
	store := &testReplayWindowHighWaterStore{}
	window, err := newReplayWindow(4, ReplayWindowState{}, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := window.Accept(0, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	previouslyAccepted := store.highWater

	saveErr := errors.New("high-water write failed")
	loadErr := errors.New("high-water load failed")
	store.saveHighWaterErr = saveErr
	store.loadHighWaterErr = loadErr
	err = window.Accept(300, func() error { return nil })
	if !errors.Is(err, saveErr) {
		t.Fatalf("save failure = %v, want %v", err, saveErr)
	}
	if !errors.Is(err, loadErr) {
		t.Fatalf("reconcile failure = %v, want %v", err, loadErr)
	}

	store.saveHighWaterErr = nil
	store.loadHighWaterErr = nil
	attemptsBeforeRetry := store.saveAttempts
	if err := window.Accept(301, func() error { return nil }); err != nil {
		t.Fatalf("accept sequence after persistence failure: %v", err)
	}
	if store.saveAttempts != attemptsBeforeRetry+1 {
		t.Fatalf("high-water save attempts = %d, want %d", store.saveAttempts, attemptsBeforeRetry+1)
	}
	if store.highWater <= previouslyAccepted {
		t.Fatalf("persisted high-water mark = %d, want greater than %d", store.highWater, previouslyAccepted)
	}

	restarted := store.restart(t, 4)
	if err := restarted.Accept(0, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed previously accepted sequence after restart error = %v, want ErrReplay", err)
	}
	if err := restarted.Accept(301, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed retried sequence after restart error = %v, want ErrReplay", err)
	}
}

func TestReplayWindowStoreRestoresState(t *testing.T) {
	store := &testReplayWindowStore{}
	window, err := newReplayWindow(4, store.state, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if acceptErr := window.Accept(9, func() error { return nil }); acceptErr != nil {
		t.Fatal(acceptErr)
	}

	restarted, err := newReplayWindow(4, store.state, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Accept(9, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed sequence after restart error = %v, want ErrReplay", err)
	}
}

func TestReplayWindowStoreFailureDoesNotAdvanceWindow(t *testing.T) {
	saveErr := errors.New("storage unavailable")
	store := &testReplayWindowStore{saveErr: saveErr}
	window, err := newReplayWindow(4, store.state, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := window.Accept(9, func() error { return nil }); !errors.Is(err, saveErr) {
		t.Fatalf("persistence error = %v, want %v", err, saveErr)
	}

	store.saveErr = nil
	if err := window.Accept(9, func() error { return nil }); err != nil {
		t.Fatalf("sequence consumed after persistence failure: %v", err)
	}
}

func TestReplayWindowRecoversLiveStateAfterInterruptedSave(t *testing.T) {
	saveErr := errors.New("guard write failed after transaction journal")
	store := &testReplayWindowStore{
		state: ReplayWindowState{
			HighestSequence: 0,
			Bitmap:          1,
			Started:         true,
			WindowSize:      4,
		},
		saveErr:           saveErr,
		commitOnSaveError: true,
	}
	window, err := newReplayWindow(4, store.state, store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := window.Accept(1, func() error { return nil }); !errors.Is(err, saveErr) {
		t.Fatalf("interrupted save error = %v, want %v", err, saveErr)
	}

	store.saveErr = nil
	if err := window.Accept(2, func() error { return nil }); err != nil {
		t.Fatalf("accept next sequence after recovery: %v", err)
	}
	if got, want := store.state, (ReplayWindowState{
		HighestSequence: 2,
		Bitmap:          7,
		Started:         true,
		WindowSize:      4,
	}); got != want {
		t.Fatalf("persisted replay state = %+v, want %+v", got, want)
	}
	if err := window.Accept(1, func() error { return nil }); !errors.Is(err, ErrReplay) {
		t.Fatalf("recovered sequence replay error = %v, want ErrReplay", err)
	}
}

func TestContextLoadsReplayWindowStore(t *testing.T) {
	loadErr := errors.New("storage unavailable")
	cfg := ContextConfig{
		MasterSecret:      []byte("0123456789abcdef"),
		SenderID:          []byte{1},
		RecipientID:       []byte{2},
		ReplayWindowStore: &testReplayWindowStore{loadErr: loadErr},
	}
	if _, err := NewContext(cfg); !errors.Is(err, loadErr) {
		t.Fatalf("context load error = %v, want %v", err, loadErr)
	}

	cfg.ReplayWindowStore = &testReplayWindowStore{
		state: ReplayWindowState{HighestSequence: 4, Bitmap: 2, Started: true},
	}
	if _, err := NewContext(cfg); !errors.Is(err, ErrInvalidReplayState) {
		t.Fatalf("invalid replay state error = %v, want ErrInvalidReplayState", err)
	}
}
