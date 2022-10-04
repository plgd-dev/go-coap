package config

import (
	"context"
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/client"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/runner/periodic"
)

type (
	ErrorFunc                                   = func(error)
	HandlerFunc[C responsewriter.Client]        func(w *responsewriter.ResponseWriter[C], r *pool.Message)
	ProcessRequestFunc[C responsewriter.Client] func(req *pool.Message, cc C, handler HandlerFunc[C])
	GoPoolFunc[C responsewriter.Client]         func(processReqFunc ProcessRequestFunc[C], req *pool.Message, cc C, handler HandlerFunc[C]) error
)

type Common[C responsewriter.Client] struct {
	LimitClientParallelRequests         int64
	LimitClientEndpointParallelRequests int64
	Ctx                                 context.Context
	Errors                              ErrorFunc
	GoPool                              GoPoolFunc[C]
	PeriodicRunner                      periodic.Func
	MessagePool                         *pool.Pool
	GetToken                            client.GetTokenFunc
	MaxMessageSize                      uint32
	BlockwiseTransferTimeout            time.Duration
	BlockwiseSZX                        blockwise.SZX
	BlockwiseEnable                     bool
}

func NewCommon[C responsewriter.Client]() Common[C] {
	return Common[C]{
		Ctx:            context.Background(),
		MaxMessageSize: 64 * 1024,
		Errors: func(err error) {
			fmt.Println(err)
		},
		GoPool: func(processReqFunc ProcessRequestFunc[C], req *pool.Message, cc C, handler HandlerFunc[C]) error {
			go processReqFunc(req, cc, handler)
			return nil
		},
		BlockwiseSZX:             blockwise.SZX1024,
		BlockwiseEnable:          true,
		BlockwiseTransferTimeout: time.Second * 3,
		PeriodicRunner: func(f func(now time.Time) bool) {
			go func() {
				for f(time.Now()) {
					time.Sleep(4 * time.Second)
				}
			}()
		},
		MessagePool:                         pool.New(1024, 2048),
		GetToken:                            message.GetToken,
		LimitClientParallelRequests:         1,
		LimitClientEndpointParallelRequests: 1,
	}
}
