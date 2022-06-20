package inactivity

import (
	"unsafe"

	"go.uber.org/atomic"
)

type cancelPingFunc func()

type KeepAlive struct {
	pongToken  atomic.Uint64
	onInactive OnInactiveFunc

	sendPing   func(cc ClientConn, receivePong func()) (func(), error)
	cancelPing atomic.UnsafePointer
	numFails   atomic.Uint32

	maxRetries uint32
}

func NewKeepAlive(maxRetries uint32, onInactive OnInactiveFunc, sendPing func(cc ClientConn, receivePong func()) (func(), error)) *KeepAlive {
	return &KeepAlive{
		maxRetries: maxRetries,
		sendPing:   sendPing,
		onInactive: onInactive,
	}
}

func (m *KeepAlive) checkCancelPing() {
	cancelPingPtr := m.cancelPing.Swap(nil)
	if cancelPingPtr != nil {
		cancelPing := *(*cancelPingFunc)(cancelPingPtr)
		cancelPing()
	}
}

func (m *KeepAlive) OnInactive(cc ClientConn) {
	v := m.incrementFails()
	m.checkCancelPing()
	if v > m.maxRetries {
		m.onInactive(cc)
		return
	}
	pongToken := m.pongToken.Add(1)
	cancel, err := m.sendPing(cc, func() {
		if m.pongToken.Load() == pongToken {
			m.resetFails()
		}
	})
	if err != nil {
		return
	}
	m.cancelPing.Store(unsafe.Pointer(&cancel))
}

func (m *KeepAlive) incrementFails() uint32 {
	return m.numFails.Add(1)
}

func (m *KeepAlive) resetFails() {
	m.numFails.Store(0)
}
