package server

import (
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/options/config"
	udpClient "github.com/plgd-dev/go-coap/v2/udp/client"
)

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*responsewriter.ResponseWriter[*udpClient.ClientConn], *pool.Message)

type ErrorFunc = func(error)

type GoPoolFunc = func(func()) error

// OnNewClientConnFunc is the callback for new connections.
type OnNewClientConnFunc = func(cc *udpClient.ClientConn)

type GetMIDFunc = func() int32

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.DefaultCommon(),
		CreateInactivityMonitor: func() udpClient.InactivityMonitor {
			timeout := time.Second * 16
			onInactive := func(cc *udpClient.ClientConn) {
				_ = cc.Close()
			}
			return inactivity.NewInactivityMonitor(timeout, onInactive)
		},
		OnNewClientConn: func(cc *udpClient.ClientConn) {
			// do nothing by default
		},
		TransmissionNStart:             time.Second,
		TransmissionAcknowledgeTimeout: time.Second * 2,
		TransmissionMaxRetransmit:      4,
		GetMID:                         message.GetMID,
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*udpClient.ClientConn], r *pool.Message) {
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			opts.Errors(fmt.Errorf("dtls server: cannot set response: %w", err))
		}
	}
	return opts
}()

type Config struct {
	config.Common
	CreateInactivityMonitor        func() udpClient.InactivityMonitor
	GetMID                         GetMIDFunc
	Handler                        HandlerFunc
	OnNewClientConn                OnNewClientConnFunc
	TransmissionNStart             time.Duration
	TransmissionAcknowledgeTimeout time.Duration
	TransmissionMaxRetransmit      uint32
}
