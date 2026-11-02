package client

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestConnDropsPendingDuplicates(t *testing.T) {
	for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
		t.Run(typ.String(), func(t *testing.T) {
			session := newRecordingSession()
			defer func() { require.NoError(t, session.Close()) }()
			started := make(chan struct{})
			release := make(chan struct{})
			var unblock atomic.Bool
			unblockHandler := func() {
				if unblock.CompareAndSwap(false, true) {
					close(release)
				}
			}
			defer unblockHandler()
			completed := make(chan int32, 8)
			var calls atomic.Int32
			cfg := DefaultConfig
			cfg.Errors = func(err error) { assert.NoError(t, err) }
			cfg.ReceivedMessageQueueSize = 8
			cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				if calls.Inc() == 1 {
					close(started)
					<-release
				}
				assert.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
			}
			cfg.ProcessReceivedMessage = func(req *pool.Message, cc *Conn, handler config.HandlerFunc[*Conn]) {
				mid := req.MessageID()
				cc.ProcessReceivedMessageWithHandler(req, handler)
				completed <- mid
			}
			cc := NewConnWithOpts(session, &cfg)
			req := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(req)
			req.SetType(typ)
			req.SetCode(codes.GET)
			req.SetToken([]byte{1})
			req.SetMessageID(42)
			datagram, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.NoError(t, cc.Process(nil, datagram))
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler did not start")
			}
			for i := 0; i < 3; i++ {
				require.NoError(t, cc.Process(nil, datagram))
			}
			// A different MID is a new request and must not be dropped.
			req.SetMessageID(43)
			next, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.NoError(t, cc.Process(nil, next))
			unblockHandler()
			for mid := int32(0); mid != 43; {
				select {
				case mid = <-completed:
				case <-time.After(time.Second):
					t.Fatal("queued requests were not processed")
				}
			}
			require.Equal(t, int32(2), calls.Load())
			require.Equal(t, 2, session.writtenCount(), "pending retransmissions must not produce queued replies")

			// A retry after completion must still receive the cached response.
			req.SetMessageID(42)
			datagram, err = req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.NoError(t, cc.Process(nil, datagram))
			select {
			case mid := <-completed:
				require.Equal(t, int32(42), mid)
			case <-time.After(time.Second):
				t.Fatal("cached retry was not processed")
			}
			require.Equal(t, int32(2), calls.Load())
			require.Equal(t, 3, session.writtenCount())
			require.Equal(t, codes.Content, session.lastWritten().Code())
		})
	}
}
