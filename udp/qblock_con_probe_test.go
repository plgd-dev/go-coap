package udp_test

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestQBlockServerCONProbeUDP(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "piggyback", true: "separate"}[delayed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			sc := qblock.DefaultServerConfig()
			sc.ProbingRate = 65536
			var probes, gets, posts atomic.Int32
			s := udp.NewServer(options.WithQBlockServer(sc), options.WithErrors(func(err error) { t.Logf("server error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
				path, _ := r.Path()
				if path == "/probe" {
					probes.Add(1)
					if delayed {
						select {
						case <-time.After(1200 * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					}
				} else if r.Code() == codes.GET {
					gets.Add(1)
				} else {
					posts.Add(1)
					body, err := r.ReadBody()
					require.NoError(t, err)
					require.Equal(t, bytes.Repeat([]byte{'u'}, 40), body)
				}
				code := codes.Content
				if r.Code() != codes.GET {
					code = codes.Changed
				}
				require.NoError(t, w.SetResponse(code, message.TextPlain, bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz0123456789ABCD"))))
			}))
			l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- s.Serve(l) }()
			defer func() { s.Stop(); require.NoError(t, <-done) }()
			cfg := qblock.DefaultClientConfig()
			cfg.Mode = qblock.Require
			cfg.ProbingRate = 65536
			cc, err := udp.Dial(l.LocalAddr().String(), options.WithContext(ctx), options.WithQBlock(cfg), options.WithErrors(func(err error) { t.Logf("client error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second))
			require.NoError(t, err)
			defer cc.Close()
			supported, err := cc.ProbeQBlock(ctx, "/probe")
			require.NoError(t, err)
			require.True(t, supported)
			resp, err := cc.Get(ctx, "/get")
			require.NoError(t, err)
			payload, err := io.ReadAll(resp.Body())
			require.NoError(t, err)
			require.Equal(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCD", string(payload))
			cc.ReleaseMessage(resp)
			resp, err = cc.Post(ctx, "/upload", message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'u'}, 40)))
			require.NoError(t, err)
			require.Equal(t, codes.Changed, resp.Code())
			cc.ReleaseMessage(resp)
			require.EqualValues(t, 1, probes.Load())
			require.EqualValues(t, 1, gets.Load())
			require.EqualValues(t, 1, posts.Load())
		})
	}
}
