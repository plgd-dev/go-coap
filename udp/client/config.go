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

const DefaultMTU = 1472

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.NewCommon[*Conn](),
		CreateInactivityMonitor: func() InactivityMonitor {
			return inactivity.NewNilMonitor[*Conn]()
		},
		Dialer:                         &net.Dialer{Timeout: time.Second * 3},
		Net:                            "udp",
		TransmissionNStart:             1,
		TransmissionAcknowledgeTimeout: time.Second * 2,
		TransmissionMaxRetransmit:      4,
		GetMID:                         message.GetMID,
		MTU:                            DefaultMTU,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
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
	CreateInactivityMonitor CreateInactivityMonitorFunc
	GetMID                  GetMIDFunc
	Handler                 HandlerFunc
	Dialer                  *net.Dialer
	Net                     string
	config.Common[*Conn]
	TransmissionAcknowledgeTimeout time.Duration
	TransmissionNStart             uint32
	TransmissionMaxRetransmit      uint32
	MTU                            uint16
	CloseSocket                    bool
}
