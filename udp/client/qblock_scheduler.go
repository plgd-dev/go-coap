package client

import (
	"sync"
	"time"
)

type qblockClock interface {
	Now() time.Time
	NewTimer() qblockTimer
}

type qblockTimer interface {
	C() <-chan time.Time
	Reset(time.Duration)
	Stop() bool
}

type qblockScheduleMode uint8

const (
	qblockScheduleManual qblockScheduleMode = iota
	qblockScheduleAutomatic
)

type qblockScheduler struct {
	clock    qblockClock
	timer    qblockTimer
	notify   chan struct{}
	due      chan struct{}
	done     chan struct{}
	stop     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
}

type qblockCallbackDispatcher struct {
	mu       sync.Mutex
	queue    chan qblockQueuedCallback
	stopCh   chan struct{}
	stopped  chan struct{}
	stopping bool
	stopOnce sync.Once
}

type qblockQueuedCallback struct {
	run     func()
	discard func()
}

func newQBlockCallbackDispatcher(limit uint32) *qblockCallbackDispatcher {
	d := &qblockCallbackDispatcher{
		queue:   make(chan qblockQueuedCallback, limit),
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *qblockCallbackDispatcher) submit(callback func()) bool {
	return d.submitWithDiscard(callback, nil)
}

func (d *qblockCallbackDispatcher) submitWithDiscard(callback, discard func()) bool {
	if callback == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return false
	}
	select {
	case d.queue <- qblockQueuedCallback{run: callback, discard: discard}:
		return true
	default:
		return false
	}
}

func (d *qblockCallbackDispatcher) stop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.stopping = true
		close(d.stopCh)
		var discarded []func()
		for {
			select {
			case callback := <-d.queue:
				if callback.discard != nil {
					discarded = append(discarded, callback.discard)
				}
			default:
				d.mu.Unlock()
				for _, discard := range discarded {
					discard()
				}
				return
			}
		}
	})
}

func (d *qblockCallbackDispatcher) run() {
	defer close(d.stopped)
	for {
		select {
		case <-d.stopCh:
			return
		default:
		}
		select {
		case <-d.stopCh:
			return
		case callback := <-d.queue:
			d.mu.Lock()
			stopping := d.stopping
			d.mu.Unlock()
			if stopping {
				if callback.discard != nil {
					callback.discard()
				}
				return
			}
			callback.run()
		}
	}
}

func newQBlockScheduler(clock qblockClock) *qblockScheduler {
	return &qblockScheduler{
		clock:   clock,
		timer:   clock.NewTimer(),
		notify:  make(chan struct{}, 1),
		due:     make(chan struct{}, 1),
		done:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

type qblockCallbackSlots struct {
	mu    sync.Mutex
	limit uint32
	used  uint32
}

func newQBlockCallbackSlots(limit uint32) *qblockCallbackSlots {
	return &qblockCallbackSlots{limit: limit}
}

func (s *qblockCallbackSlots) tryAcquire() (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used >= s.limit {
		return nil, false
	}
	s.used++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.used--
			s.mu.Unlock()
		})
	}, true
}

type realQBlockClock struct{}

func (realQBlockClock) Now() time.Time { return time.Now() }

func (realQBlockClock) NewTimer() qblockTimer {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	return realQBlockTimer{timer: timer}
}

type realQBlockTimer struct{ timer *time.Timer }

func (t realQBlockTimer) C() <-chan time.Time   { return t.timer.C }
func (t realQBlockTimer) Reset(d time.Duration) { t.timer.Reset(d) }
func (t realQBlockTimer) Stop() bool            { return t.timer.Stop() }
