package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
)

var DefaultConfig = func() Config {
	opts := Config{
		Ctx:            context.Background(),
		MaxMessageSize: 64 * 1024,
		Errors: func(err error) {
			fmt.Println(err)
		},
		GoPool: func(f func()) error {
			go func() {
				f()
			}()
			return nil
		},
		PeriodicRunner: func(f func(now time.Time) bool) {
			go func() {
				for f(time.Now()) {
					time.Sleep(4 * time.Second)
				}
			}()
		},
		Dialer:                         &net.Dialer{Timeout: time.Second * 3},
		Net:                            "udp",
		BlockwiseSZX:                   blockwise.SZX1024,
		BlockwiseEnable:                true,
		BlockwiseTransferTimeout:       time.Second * 3,
		TransmissionNStart:             time.Second,
		TransmissionAcknowledgeTimeout: time.Second * 2,
		TransmissionMaxRetransmit:      4,
		GetMID:                         message.GetMID,
		CreateInactivityMonitor: func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		},
		MessagePool: pool.New(1024, 1600),
		GetToken:    message.GetToken,
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
	Net                            string
	Ctx                            context.Context
	GetMID                         GetMIDFunc
	Handler                        HandlerFunc
	Errors                         ErrorFunc
	GoPool                         GoPoolFunc
	Dialer                         *net.Dialer
	PeriodicRunner                 periodic.Func
	MessagePool                    *pool.Pool
	BlockwiseTransferTimeout       time.Duration
	TransmissionNStart             time.Duration
	TransmissionAcknowledgeTimeout time.Duration
	CreateInactivityMonitor        func() inactivity.Monitor
	MaxMessageSize                 uint32
	TransmissionMaxRetransmit      uint32
	CloseSocket                    bool
	BlockwiseSZX                   blockwise.SZX
	BlockwiseEnable                bool
	GetToken                       client.GetTokenFunc
}
