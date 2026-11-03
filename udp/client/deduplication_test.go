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

func pendingMessageCount(cc *Conn) int {
	cc.pendingMessagesMutex.Lock()
	defer cc.pendingMessagesMutex.Unlock()
	return len(cc.pendingMessages)
}

func waitForNoPendingMessages(t *testing.T, cc *Conn) {
	t.Helper()
	require.Eventually(t, func() bool { return pendingMessageCount(cc) == 0 }, time.Second, time.Millisecond)
}

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
			req.SetMessageID(43)
			next, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.NoError(t, cc.Process(nil, next))
			require.Equal(t, 2, pendingMessageCount(cc))
			unblockHandler()
			for mid := int32(0); mid != 43; {
				select {
				case mid = <-completed:
				case <-time.After(time.Second):
					t.Fatal("queued requests were not processed")
				}
			}
			waitForNoPendingMessages(t, cc)
			require.Equal(t, int32(2), calls.Load())
			require.Equal(t, 2, session.writtenCount(), "pending retransmissions must not produce queued replies")

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
			waitForNoPendingMessages(t, cc)
			require.Equal(t, int32(2), calls.Load())
			require.Equal(t, 3, session.writtenCount())
			require.Equal(t, codes.Content, session.lastWritten().Code())
		})
	}
}

func TestConnPendingDuplicateCustomProcessor(t *testing.T) {
	for _, name := range []string{"synchronous", "asynchronous"} {
		t.Run(name, func(t *testing.T) {
			session := newRecordingSession()
			defer func() { require.NoError(t, session.Close()) }()
			completed := make(chan struct{}, 1)
			cfg := DefaultConfig
			cfg.ProcessReceivedMessage = func(req *pool.Message, cc *Conn, _ config.HandlerFunc[*Conn]) {
				release := func() {
					req.Reset()
					cc.ReleaseMessage(req)
					completed <- struct{}{}
				}
				if name == "asynchronous" {
					go release()
				} else {
					release()
				}
			}
			cc := NewConnWithOpts(session, &cfg)
			req := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(req)
			req.SetType(message.Confirmable)
			req.SetCode(codes.GET)
			req.SetToken([]byte{1})
			req.SetMessageID(42)
			datagram, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			for i := 0; i < 2; i++ {
				require.NoError(t, cc.Process(nil, datagram))
				select {
				case <-completed:
				case <-time.After(time.Second):
					t.Fatal("custom processor did not complete")
				}
				waitForNoPendingMessages(t, cc)
			}
		})
	}
}

func TestConnAsyncProcessorReturnEndsPendingTracking(t *testing.T) {
	session := newRecordingSession()
	defer func() { require.NoError(t, session.Close()) }()
	started := make(chan struct{}, 2)
	completed := make(chan struct{}, 2)
	release := make(chan struct{})
	var unblock atomic.Bool
	unblockHandlers := func() {
		if unblock.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblockHandlers()
	cfg := DefaultConfig
	cfg.Errors = func(err error) { assert.NoError(t, err) }
	cfg.ProcessReceivedMessage = func(req *pool.Message, cc *Conn, _ config.HandlerFunc[*Conn]) {
		go func() {
			cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				w.Message().SetModified(false)
				started <- struct{}{}
				<-release
			})
			completed <- struct{}{}
		}()
	}
	cc := NewConnWithOpts(session, &cfg)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(42)
	datagram, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		require.NoError(t, cc.Process(nil, datagram))
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("asynchronous handler did not start")
		}
		waitForNoPendingMessages(t, cc)
	}
	// Both handlers are still blocked, but their dispatch callbacks returned.
	require.Empty(t, completed)
	unblockHandlers()
	for i := 0; i < 2; i++ {
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Fatal("asynchronous handler did not finish")
		}
	}
}

func TestConnHijackedRequestDoesNotRetainPendingState(t *testing.T) {
	for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
		t.Run(typ.String(), func(t *testing.T) {
			session := newRecordingSession()
			defer func() { require.NoError(t, session.Close()) }()
			hijacked := make(chan *pool.Message, 1)
			completed := make(chan struct{}, 1)
			var calls atomic.Int32
			cfg := DefaultConfig
			cfg.Errors = func(err error) { assert.NoError(t, err) }
			cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
				calls.Inc()
				r.Hijack()
				hijacked <- r
				assert.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
			}
			cfg.ProcessReceivedMessage = func(req *pool.Message, cc *Conn, handler config.HandlerFunc[*Conn]) {
				cc.ProcessReceivedMessageWithHandler(req, handler)
				completed <- struct{}{}
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
			case owned := <-hijacked:
				defer cc.ReleaseMessage(owned)
			case <-time.After(time.Second):
				t.Fatal("handler did not hijack request")
			}
			for i := 0; i < 2; i++ {
				select {
				case <-completed:
				case <-time.After(time.Second):
					t.Fatal("processor did not complete")
				}
				waitForNoPendingMessages(t, cc)
				if i == 0 {
					require.NoError(t, cc.Process(nil, datagram))
				}
			}
			require.Equal(t, int32(1), calls.Load(), "completed retries must use the response cache")
			require.Equal(t, 2, session.writtenCount())
		})
	}
}

func TestConnPendingMessagesBoundAndExpire(t *testing.T) {
	session := newRecordingSession()
	defer func() { require.NoError(t, session.Close()) }()
	cfg := DefaultConfig
	cfg.MaxPendingMessages = 3
	reported := make(chan error, 1)
	cfg.Errors = func(err error) { reported <- err }
	cc := NewConnWithOpts(session, &cfg)
	now := time.Now()
	for mid := int32(0); mid < int32(cfg.MaxPendingMessages); mid++ {
		duplicate, err := cc.addPendingMessage(mid, uint64(mid)+1, now)
		require.NoError(t, err)
		require.False(t, duplicate)
	}
	require.Equal(t, int(cfg.MaxPendingMessages), pendingMessageCount(cc))
	duplicate, err := cc.addPendingMessage(0, 2000, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, duplicate, "duplicates must still be suppressed at capacity")

	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(int32(cfg.MaxPendingMessages))
	datagram, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.NoError(t, cc.Process(nil, datagram), "overload must not terminate the transport session")
	select {
	case reportedErr := <-reported:
		require.ErrorContains(t, reportedErr, "in-flight message limit (3) exceeded")
	default:
		t.Fatal("overload was not reported")
	}
	require.Equal(t, int(cfg.MaxPendingMessages), pendingMessageCount(cc))

	cc.CheckExpirations(now.Add(ExchangeLifetime - time.Nanosecond))
	require.Equal(t, int(cfg.MaxPendingMessages), pendingMessageCount(cc))
	cc.CheckExpirations(now.Add(ExchangeLifetime))
	require.Zero(t, pendingMessageCount(cc))

	duplicate, err = cc.addPendingMessage(42, 2001, now)
	require.NoError(t, err)
	require.False(t, duplicate)
	duplicate, err = cc.addPendingMessage(42, 2002, now.Add(ExchangeLifetime))
	require.NoError(t, err)
	require.False(t, duplicate, "admission must reclaim expired state without a periodic runner")
	cc.deletePendingMessage(42, 2001)
	require.Equal(t, 1, pendingMessageCount(cc), "stale cleanup must preserve a reused MID")
	cc.deletePendingMessage(42, 2002)
	require.Zero(t, pendingMessageCount(cc))
	_, err = cc.addPendingMessage(42, 2003, now)
	require.NoError(t, err)
	require.NoError(t, cc.Close())
	require.Zero(t, pendingMessageCount(cc))
}

func TestConnPendingMessagesPruneOnlyAtCapacity(t *testing.T) {
	cc := &Conn{maxPendingMessages: 2}
	now := time.Now()
	duplicate, err := cc.addPendingMessage(1, 1, now)
	require.NoError(t, err)
	require.False(t, duplicate)

	later := now.Add(ExchangeLifetime)
	duplicate, err = cc.addPendingMessage(2, 2, later)
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, 2, pendingMessageCount(cc), "below capacity admission must not scan expired entries")

	duplicate, err = cc.addPendingMessage(3, 3, later)
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, 2, pendingMessageCount(cc))
	cc.pendingMessagesMutex.Lock()
	_, expiredPresent := cc.pendingMessages[1]
	_, activePresent := cc.pendingMessages[2]
	_, admittedPresent := cc.pendingMessages[3]
	cc.pendingMessagesMutex.Unlock()
	require.False(t, expiredPresent)
	require.True(t, activePresent)
	require.True(t, admittedPresent)
}

func TestConnPendingMessagesDisabled(t *testing.T) {
	cfg := DefaultConfig
	cfg.MaxPendingMessages = 0
	session := newRecordingSession()
	defer func() { require.NoError(t, session.Close()) }()
	cc := NewConnWithOpts(session, &cfg)
	for i := 0; i < 3; i++ {
		duplicate, err := cc.addPendingMessage(42, uint64(i), time.Now())
		require.NoError(t, err)
		require.False(t, duplicate)
	}
	require.Zero(t, pendingMessageCount(cc))
}

func TestConnStaleReceiveCleanupDoesNotResetMessage(t *testing.T) {
	session := newRecordingSession()
	defer func() { require.NoError(t, session.Close()) }()
	cfg := DefaultConfig
	cfg.MessagePool = pool.New(0, 0)
	cc := NewConnWithOpts(session, &cfg)
	req := cc.AcquireMessage(context.Background())
	req.SetMessageID(42)
	cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		r.Hijack()
		cc.ReleaseMessage(r)
		r.Reset()
		r.SetMessageID(43)
		r.SetCode(codes.GET)
		w.Message().SetType(message.Acknowledgement)
		w.Message().SetCode(codes.Empty)
		w.Message().SetMessageID(42)
	})
	require.Equal(t, int32(43), req.MessageID())
	require.Equal(t, codes.GET, req.Code())
	require.Equal(t, 1, session.writtenCount())
}
