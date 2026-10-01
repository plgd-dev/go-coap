package client

import (
	"bytes"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func delayedCONHarness(t *testing.T) (*serverHarness, chan struct{}, chan struct{}) {
	t.Helper()
	entered := make(chan struct{})
	resume := make(chan struct{})
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		close(entered)
		<-resume
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("abcdefghijklmnopqrst")))
	})
	done := make(chan struct{})
	go func() { h.ingest(conGET(t, h, 70, 1, 0)); close(done) }()
	<-entered
	return h, resume, done
}
func conFeedback(t *testing.T, h *serverHarness, mid int32, typ message.Type) {
	t.Helper()
	msg := h.cc.AcquireMessage(h.cc.Context())
	msg.SetType(typ)
	msg.SetMessageID(mid)
	msg.SetCode(codes.Empty)
	h.ingest(msg)
}
func TestQBlockServerCONDelayedSeparate(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.advance(time.Second)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.Empty, writes[0].code)
	require.Empty(t, writes[0].token)
	close(resume)
	<-done
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 2)
	require.Equal(t, message.Confirmable, writes[1].typ)
	require.NotEqualValues(t, 70, writes[1].mid)
	require.Equal(t, []byte{1}, []byte(writes[1].token))
	require.EqualValues(t, 8, writes[1].block)
	h.ingest(conGET(t, h, 70, 1, 0))
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 3)
	require.Equal(t, writes[0], writes[2])
	conFeedback(t, h, writes[1].mid, message.Acknowledgement)
}
func TestQBlockServerCONRetransmit(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.advance(time.Second)
	close(resume)
	<-done
	h.advance(2 * time.Second)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3)
	require.Equal(t, writes[1], writes[2])
	h.advance(4 * time.Second)
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 4)
	require.Equal(t, writes[1], writes[3])
	conFeedback(t, h, writes[1].mid, message.Acknowledgement)
	h.advance(8 * time.Second)
	require.Len(t, h.session.writesSnapshot(), 4)
}
func TestQBlockServerCONResetAndExhaustion(t *testing.T) {
	for _, reset := range []bool{true, false} {
		t.Run(map[bool]string{true: "reset", false: "exhaustion"}[reset], func(t *testing.T) {
			h, resume, done := delayedCONHarness(t)
			h.cc.Transmission().SetTransmissionMaxRetransmit(1)
			h.advance(time.Second)
			close(resume)
			<-done
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 2)
			if reset {
				conFeedback(t, h, writes[1].mid, message.Reset)
				h.advance(10 * time.Second)
				require.Len(t, h.session.writesSnapshot(), 2)
			} else {
				h.advance(2 * time.Second)
				require.Len(t, h.session.writesSnapshot(), 3)
				h.advance(4 * time.Second)
				h.advance(8 * time.Second)
				require.Len(t, h.session.writesSnapshot(), 3)
			}
		})
	}
}
func TestQBlockServerCONExpiryAndClose(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "close"}[closed], func(t *testing.T) {
			h, resume, done := delayedCONHarness(t)
			budget := h.cc.qblockClient.ownedBudget
			budget.mu.Lock()
			used := budget.used
			budget.mu.Unlock()
			if closed {
				h.cc.qblockClient.close()
			} else {
				h.advance(ExchangeLifetime)
			}
			budget.mu.Lock()
			require.Equal(t, used, budget.used)
			budget.mu.Unlock()
			before := len(h.session.writesSnapshot())
			close(resume)
			<-done
			require.Len(t, h.session.writesSnapshot(), before)
			budget.mu.Lock()
			require.Equal(t, budget.floor, budget.used)
			budget.mu.Unlock()
		})
	}
}
func TestQBlockServerCONDeadlineRace(t *testing.T) {
	for i := 0; i < 10; i++ {
		clock := newFakeQBlockClock(time.Unix(100, 0))
		var calls atomic.Int32
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			calls.Add(1)
			w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
		})
		h.cc.qblockClient.now = clock.Now
		done := make(chan struct{})
		go func() { h.ingest(conGET(t, h, 70, 1, 0)); close(done) }()
		clock.Advance(time.Second)
		h.cc.qblockClient.advanceDue(clock.Now())
		<-done
		writes := h.session.writesSnapshot()
		require.EqualValues(t, 1, calls.Load())
		require.NotEmpty(t, writes)
		content := 0
		for _, w := range writes {
			if w.code == codes.Content {
				content++
			}
		}
		require.Equal(t, 1, content)
		if len(writes) == 2 {
			require.Equal(t, codes.Empty, writes[0].code)
			require.Equal(t, message.Confirmable, writes[1].typ)
		} else {
			require.Len(t, writes, 1)
			require.Equal(t, message.Acknowledgement, writes[0].typ)
		}
	}
}

func TestQBlockServerCONEmptyACKPrecedesSeparate(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.session.firstWriteStarted = make(chan struct{}, 1)
	h.session.releaseFirstWrite = make(chan struct{})
	tickDone := make(chan struct{})
	go func() { h.cc.qblockClient.advanceDue(h.now.Add(time.Second)); close(tickDone) }()
	<-h.session.firstWriteStarted
	close(resume)
	// Callback must not publish Content ahead of the blocked empty ACK.
	require.Empty(t, h.session.writesSnapshot())
	close(h.session.releaseFirstWrite)
	<-tickDone
	<-done
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 2)
	require.Equal(t, codes.Empty, writes[0].code)
	require.Equal(t, message.Confirmable, writes[1].typ)
}
