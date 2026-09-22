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
