package server_test

import (
	"testing"
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
)

func TestDefaultInactivityTimeoutCoversRetransmissionWindow(t *testing.T) {
	tests := []struct {
		name                    string
		createInactivityMonitor func() client.InactivityMonitor
	}{
		{name: "UDP", createInactivityMonitor: udpServer.DefaultConfig.CreateInactivityMonitor},
		{name: "DTLS", createInactivityMonitor: dtlsServer.DefaultConfig.CreateInactivityMonitor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.createInactivityMonitor().CheckInactivity(time.Now().Add(50*time.Second), nil)
		})
	}
}
