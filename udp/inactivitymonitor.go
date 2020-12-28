package udp

import (
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"time"
)

type OnInactiveFunc func(cc *client.ClientConn)

type inactivityMonitor struct {
	inactiveInterval time.Duration
	onInactive       OnInactiveFunc
}

func newInactivityMonitor() *inactivityMonitor {
	return &inactivityMonitor{
		inactiveInterval: 10 * time.Minute,
		onInactive: func(cc *client.ClientConn) {
			cc.Close()
		},
	}
}

func (m *inactivityMonitor) Run(cc *client.ClientConn) error {
	if m.onInactive == nil || m.inactiveInterval == time.Duration(0) {
		return nil
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		timeout := time.Until(cc.LastActivity().Add(m.inactiveInterval))
		if timeout <= 0 {
			timeout = m.inactiveInterval
		}
		select {
		case <-time.After(timeout):
			if time.Since(cc.LastActivity()) >= m.inactiveInterval {
				m.onInactive(cc)
			}
		case <-cc.Context().Done():
			return nil
		}
	}
}
