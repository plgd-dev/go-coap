package udp

import (
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"sync/atomic"
	"time"
)

type OnInactiveFunc func(cc *client.ClientConn)

type InactivityMonitor interface {
	Run(cc *client.ClientConn) error
	Notify()
}

type inactivityMonitor struct {
	inactiveInterval time.Duration
	onInactive       OnInactiveFunc
	// lastActivity stores time.Time
	lastActivity atomic.Value
}

func (m *inactivityMonitor) Notify() {
	m.lastActivity.Store(time.Now())
}

func (m *inactivityMonitor) LastActivity() time.Time {
	if t, ok := m.lastActivity.Load().(time.Time); ok {
		return t
	}
	return time.Time{}
}

func closeClientConn(cc *client.ClientConn) {
	cc.Close()
}

func NewInactivityMonitor(interval time.Duration, onInactive OnInactiveFunc) InactivityMonitor {
	return &inactivityMonitor{
		inactiveInterval: interval,
		onInactive:       onInactive,
	}
}

func (m *inactivityMonitor) Run(cc *client.ClientConn) error {
	if m.onInactive == nil || m.inactiveInterval == time.Duration(0) {
		return nil
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		timeout := time.Until(m.LastActivity().Add(m.inactiveInterval))
		if timeout <= 0 {
			timeout = m.inactiveInterval
		}
		select {
		case <-time.After(timeout):
			if time.Since(m.LastActivity()) >= m.inactiveInterval {
				m.onInactive(cc)
			}
		case <-cc.Context().Done():
			return nil
		}
	}
}
