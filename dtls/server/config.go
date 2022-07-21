package server

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/blockwise"
	"github.com/plgd-dev/go-coap/v2/net/client"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	udpClient "github.com/plgd-dev/go-coap/v2/udp/client"
)

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*responsewriter.ResponseWriter[*udpClient.ClientConn], *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

// OnNewClientConnFunc is the callback for new connections.
//
// Note: Calling `dtlsConn.Close()` is forbidden, and `dtlsConn` should be treated as a
// "read-only" parameter, mainly used to get the peer certificate from the underlining connection
type OnNewClientConnFunc = func(cc *udpClient.ClientConn, dtlsConn *dtls.Conn)

type GetMIDFunc = func() int32

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
		CreateInactivityMonitor: func() inactivity.Monitor {
			return inactivity.NewNilMonitor()
		},
		BlockwiseEnable:          true,
		BlockwiseSZX:             blockwise.SZX1024,
		BlockwiseTransferTimeout: time.Second * 3,
		OnNewClientConn: func(cc *udpClient.ClientConn, dtlsConn *dtls.Conn) {
			// do nothing by default
		},
		TransmissionNStart:             time.Second,
		TransmissionAcknowledgeTimeout: time.Second * 2,
		TransmissionMaxRetransmit:      4,
		GetMID:                         message.GetMID,
		PeriodicRunner: func(f func(now time.Time) bool) {
			go func() {
				for f(time.Now()) {
					time.Sleep(4 * time.Second)
				}
			}()
		},
		MessagePool: pool.New(1024, 1600),
		GetToken:    message.GetToken,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*udpClient.ClientConn], r *pool.Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			opts.Errors(fmt.Errorf("dtls server: cannot set response: %w", err))
		}
	}
	return opts
}()

type Config struct {
	Ctx                            context.Context
	MessagePool                    *pool.Pool
	BlockwiseTransferTimeout       time.Duration
	Errors                         ErrorFunc
	GoPool                         GoPoolFunc
	CreateInactivityMonitor        func() inactivity.Monitor
	PeriodicRunner                 periodic.Func
	GetMID                         GetMIDFunc
	Handler                        HandlerFunc
	OnNewClientConn                OnNewClientConnFunc
	TransmissionNStart             time.Duration
	TransmissionAcknowledgeTimeout time.Duration
	MaxMessageSize                 uint32
	TransmissionMaxRetransmit      uint32
	BlockwiseEnable                bool
	BlockwiseSZX                   blockwise.SZX
	GetToken                       client.GetTokenFunc
}
