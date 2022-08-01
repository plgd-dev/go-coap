package client

import (
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
)

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.DefaultCommon(),
		CreateInactivityMonitor: func() InactivityMonitor {
			return inactivity.NewNilMonitor[*ClientConn]()
		},
		Dialer:                         &net.Dialer{Timeout: time.Second * 3},
		Net:                            "udp",
		TransmissionNStart:             time.Second,
		TransmissionAcknowledgeTimeout: time.Second * 2,
		TransmissionMaxRetransmit:      4,
		GetMID:                         message.GetMID,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
				opts.Errors(fmt.Errorf("udp client: cannot set response: %w", err))
			}
		}
	}
	return opts
}()

type Config struct {
	config.Common
	CreateInactivityMonitor        CreateInactivityMonitorFunc
	Net                            string
	GetMID                         GetMIDFunc
	Handler                        HandlerFunc
	Dialer                         *net.Dialer
	TransmissionNStart             time.Duration
	TransmissionAcknowledgeTimeout time.Duration
	TransmissionMaxRetransmit      uint32
	CloseSocket                    bool
}