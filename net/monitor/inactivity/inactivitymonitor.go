package inactivity

import (
	"context"
	"sync/atomic"
	"time"
)

type OnInactiveFunc[C ClientConn] func(cc C)

type ClientConn = interface {
	Context() context.Context
	Close() error
}

type InactivityMonitor[C ClientConn] struct {
	lastActivity atomic.Value
	duration     time.Duration
	onInactive   OnInactiveFunc[C]
}

func (m *InactivityMonitor[C]) Notify() {
	m.lastActivity.Store(time.Now())
}

func (m *InactivityMonitor[C]) LastActivity() time.Time {
	if t, ok := m.lastActivity.Load().(time.Time); ok {
		return t
	}
	return time.Time{}
}

func CloseClientConn(cc ClientConn) {
	// call cc.Close() directly to check and handle error if necessary
	_ = cc.Close()
}

func NewInactivityMonitor[C ClientConn](duration time.Duration, onInactive OnInactiveFunc[C]) *InactivityMonitor[C] {
	m := &InactivityMonitor[C]{
		duration:   duration,
		onInactive: onInactive,
	}
	m.Notify()
	return m
}

func (m *InactivityMonitor[C]) CheckInactivity(now time.Time, cc C) {
	if m.onInactive == nil || m.duration == time.Duration(0) {
		return
	}
	if now.After(m.LastActivity().Add(m.duration)) {
		m.onInactive(cc)
	}
}

type NilMonitor[C ClientConn] struct{}

func (m *NilMonitor[C]) CheckInactivity(now time.Time, cc C) {
}

func (m *NilMonitor[C]) Notify() {
}

func NewNilMonitor[C ClientConn]() *NilMonitor[C] {
	return &NilMonitor[C]{}
}
