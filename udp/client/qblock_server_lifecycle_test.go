package client

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func serverLifecycleConfig() qblock.ManagerConfig {
	cfg := qblock.DefaultManagerConfig()
	cfg.Transfer.Lifetime = 10 * time.Second
	return cfg
}

func TestQBlockServerLifecycle(t *testing.T) {
	t.Run("terminal record frees capacity exactly at retention", func(t *testing.T) {
		calls := 0
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second, MaxRecords: 1}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			calls++
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
		})
		h.session.writeErr = io.ErrClosedPipe
		h.ingest(h.q1(t, 1, 0, false, 4, "body"))
		require.Equal(t, 1, calls)
		require.Equal(t, 1, h.snapshot().records)

		blocked := h.q1(t, 2, 0, false, 4, "body")
		require.NoError(t, blocked.SetPath("/other"))
		h.ingest(blocked)
		require.Equal(t, 1, calls)
		full := h.snapshot()
		require.Equal(t, 1, full.records)
		require.Positive(t, full.metadataBytes)

		h.advance(10 * time.Second)
		require.Zero(t, h.snapshot().records)
		require.Zero(t, h.snapshot().metadataBytes)
		h.session.writeErr = nil
		accepted := h.q1(t, 3, 0, false, 4, "body")
		require.NoError(t, accepted.SetPath("/other"))
		h.ingest(accepted)
		require.Equal(t, 2, calls)
	})

	t.Run("reset cancels only its server transfer", func(t *testing.T) {
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 32))))
		})
		h.ingest(h.q1(t, 1, 0, false, 4, "body"))
		writes := h.session.writesSnapshot()
		require.NotEmpty(t, writes)
		before := h.snapshot()
		require.Positive(t, before.active)

		reset := h.cc.AcquireMessage(context.Background())
		defer h.cc.ReleaseMessage(reset)
		reset.SetType(message.Reset)
		reset.SetMessageID(writes[0].mid)
		require.True(t, h.cc.qblockClient.handle(reset))
		after := h.snapshot()
		require.Zero(t, after.active)
		require.Zero(t, after.managerTokens)
		require.Zero(t, after.serverTokens)
		require.Positive(t, before.mids)
		require.Zero(t, after.mids)

		unknown := h.cc.AcquireMessage(context.Background())
		defer h.cc.ReleaseMessage(unknown)
		unknown.SetType(message.Reset)
		unknown.SetMessageID(999)
		require.False(t, h.cc.qblockClient.handle(unknown))
		require.Equal(t, after, h.snapshot())
	})

	t.Run("blocked handler cannot revive a closed record", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		calls := 0
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			calls++
			close(entered)
			<-release
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("late"))))
		})
		go func() {
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			close(returned)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("handler did not start")
		}

		h.advance(10 * time.Second)
		h.ingest(h.q1(t, 2, 0, false, 4, "body"))
		require.Equal(t, 1, calls)
		h.session.closeForTest()
		afterClose := h.snapshot()
		require.Zero(t, afterClose.active)
		require.Zero(t, afterClose.records)
		require.Zero(t, afterClose.managerTokens)
		require.Zero(t, afterClose.managerBytes)
		require.Zero(t, afterClose.serverTokens)
		require.Zero(t, afterClose.mids)
		require.Zero(t, afterClose.metadataBytes)

		close(release)
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("handler did not return")
		}
		require.Empty(t, h.session.writesSnapshot())
	})

	t.Run("close cleans records before a blocked write drains", func(t *testing.T) {
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
		})
		h.session.firstWriteStarted = make(chan struct{}, 1)
		h.session.releaseFirstWrite = make(chan struct{})
		ingested := make(chan struct{})
		go func() {
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			close(ingested)
		}()
		select {
		case <-h.session.firstWriteStarted:
		case <-time.After(time.Second):
			t.Fatal("response write did not block")
		}

		closed := make(chan struct{})
		go func() {
			h.session.closeForTest()
			close(closed)
		}()
		require.Eventually(t, func() bool {
			snapshot := h.snapshot()
			return snapshot.active == 0 && snapshot.records == 0 && snapshot.managerTokens == 0 && snapshot.managerBytes == 0 && snapshot.serverTokens == 0 && snapshot.mids == 0 && snapshot.metadataBytes == 0
		}, time.Second, time.Millisecond)
		close(h.session.releaseFirstWrite)
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("close did not return")
		}
		select {
		case <-ingested:
		case <-time.After(time.Second):
			t.Fatal("ingest did not return")
		}
	})
}
