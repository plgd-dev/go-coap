package udp

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/go-ocf/go-coap/v2/blockwise"
	"github.com/go-ocf/go-coap/v2/keepalive"
	"github.com/go-ocf/go-coap/v2/message"

	"github.com/go-ocf/go-coap/v2/message/codes"
	coapNet "github.com/go-ocf/go-coap/v2/net"
	"github.com/go-ocf/go-coap/v2/udp/client"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

var defaultDialOptions = dialOptions{
	ctx:            context.Background(),
	maxMessageSize: 64 * 1024,
	heartBeat:      time.Millisecond * 100,
	handler: func(w *client.ResponseWriter, r *pool.Message) {
		switch r.Code() {
		case codes.POST, codes.PUT, codes.GET, codes.DELETE:
			w.SetResponse(codes.NotFound, message.TextPlain, nil)
		}
	},
	errors: func(err error) {
		fmt.Println(err)
	},
	goPool: func(f func() error) error {
		go func() {
			err := f()
			if err != nil {
				fmt.Println(err)
			}
		}()
		return nil
	},
	dialer:                         &net.Dialer{Timeout: time.Second * 3},
	keepalive:                      keepalive.New(),
	net:                            "udp",
	blockwiseSZX:                   blockwise.SZX1024,
	blockwiseEnable:                true,
	blockwiseTransferTimeout:       time.Second * 3,
	transmissionNStart:             time.Second,
	transmissionAcknowledgeTimeout: time.Second * 2,
	transmissionMaxRetransmit:      4,
}

type dialOptions struct {
	ctx                            context.Context
	maxMessageSize                 int
	heartBeat                      time.Duration
	handler                        HandlerFunc
	errors                         ErrorFunc
	goPool                         GoPoolFunc
	dialer                         *net.Dialer
	keepalive                      *keepalive.KeepAlive
	net                            string
	blockwiseSZX                   blockwise.SZX
	blockwiseEnable                bool
	blockwiseTransferTimeout       time.Duration
	transmissionNStart             time.Duration
	transmissionAcknowledgeTimeout time.Duration
	transmissionMaxRetransmit      int
}

// A DialOption sets options such as credentials, keepalive parameters, etc.
type DialOption interface {
	applyDial(*dialOptions)
}

// Dial creates a client connection to the given target.
func Dial(target string, opts ...DialOption) (*client.ClientConn, error) {
	cfg := defaultDialOptions
	for _, o := range opts {
		o.applyDial(&cfg)
	}

	c, err := cfg.dialer.DialContext(cfg.ctx, cfg.net, target)
	if err != nil {
		return nil, err
	}
	conn, ok := c.(*net.UDPConn)
	if !ok {
		return nil, fmt.Errorf("unsupported connection type: %T", c)
	}

	addr, ok := conn.RemoteAddr().(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("cannot get target upd address")
	}
	observatioRequests := &sync.Map{}
	var blockWise *blockwise.BlockWise
	if cfg.blockwiseEnable {
		blockWise = blockwise.NewBlockWise(func(ctx context.Context) blockwise.Message {
			return pool.AcquireMessage(ctx)
		}, func(m blockwise.Message) {
			pool.ReleaseMessage(m.(*pool.Message))
		}, cfg.blockwiseTransferTimeout, cfg.errors, false, func(token message.Token) (blockwise.Message, bool) {
			msg, ok := observatioRequests.Load(token.String())
			if !ok {
				return nil, ok
			}
			return msg.(blockwise.Message), ok
		},
		)
	}

	observationTokenHandler := client.NewHandlerContainer()

	l := coapNet.NewUDPConn(cfg.net, conn, coapNet.WithHeartBeat(cfg.heartBeat), coapNet.WithErrors(cfg.errors))
	session := NewSession(cfg.ctx,
		l,
		addr,
		cfg.maxMessageSize,
	)
	cc := client.NewClientConn(session,
		observationTokenHandler, observatioRequests, cfg.transmissionNStart, cfg.transmissionAcknowledgeTimeout, cfg.transmissionMaxRetransmit,
		client.NewObservatiomHandler(observationTokenHandler, cfg.handler),
		cfg.blockwiseSZX,
		blockWise,
		cfg.goPool,
	)

	go func() {
		err := cc.Run()
		if err != nil {
			cfg.errors(err)
		}
	}()
	if cfg.keepalive != nil {
		go func() {
			err := cfg.keepalive.Run(cc)
			if err != nil {
				cfg.errors(err)
			}
		}()
	}

	return cc, nil
}
