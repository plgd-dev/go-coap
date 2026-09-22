package client

import "time"

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
