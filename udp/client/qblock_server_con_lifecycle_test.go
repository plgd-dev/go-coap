package client

import (
	"bytes"
	"context"
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

func TestQBlockServerCONExpiredHandlerRetainsAdmission(t *testing.T) {
	entered := make(chan struct{})
	resume := make(chan struct{})
	var calls atomic.Int32
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxRecords: 1}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		if calls.Add(1) == 1 {
			close(entered)
			<-resume
		}
		_ = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
	})
	firstDone := make(chan struct{})
	go func() { h.ingest(conGET(t, h, 70, 1, 0)); close(firstDone) }()
	<-entered
	h.advance(ExchangeLifetime)
	h.ingest(conGET(t, h, 71, 2, 0))
	require.EqualValues(t, 1, calls.Load())
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.ServiceUnavailable, writes[0].code)
	close(resume)
	<-firstDone
	h.ingest(conGET(t, h, 72, 3, 0))
	require.EqualValues(t, 2, calls.Load())
}

func TestQBlockServerCONHeldWriteRetainsOwnedAccounting(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.cc.errors = func(error) {}
	h.advance(time.Second)
	close(resume)
	<-done
	require.Len(t, h.session.writesSnapshot(), 2)
	budget := h.cc.qblockClient.ownedBudget
	readUsed := func() uint64 {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.used
	}
	used := readUsed()
	require.Greater(t, used, budget.floor)
	h.session.contextWriteStart = make(chan struct{}, 1)
	h.session.releaseContextWrite = make(chan struct{})
	retryDone := make(chan struct{})
	go func() { h.advance(2 * time.Second); close(retryDone) }()
	<-h.session.contextWriteStart
	// Unpublish the expired owner while its detached write lease is still held.
	expiry := h.now.Add(ExchangeLifetime)
	h.cc.qblockClient.mu.Lock()
	h.cc.qblockClient.server.expireCONLocked(expiry)
	retained := len(h.cc.qblockClient.server.conByMID)
	h.cc.qblockClient.mu.Unlock()
	held := readUsed()
	close(h.session.releaseContextWrite)
	<-retryDone
	require.Zero(t, retained)
	require.Equal(t, used, held)
	require.Equal(t, budget.floor, readUsed())
}

func TestQBlockServerCONFeedbackOwnsOnlyItsPermit(t *testing.T) {
	for _, firstFeedback := range []message.Type{message.Acknowledgement, message.Reset} {
		t.Run(map[message.Type]string{message.Acknowledgement: "ack", message.Reset: "reset"}[firstFeedback], func(t *testing.T) {
			clock := newFakeQBlockClock(time.Unix(100, 0))
			domain := newQBlockEndpointDomain(clock, qblock.DefaultServerConfig().ProbingRate, 4, 4)
			t.Cleanup(domain.close)
			entered1, entered2 := make(chan struct{}), make(chan struct{})
			resume1, resume2 := make(chan struct{}), make(chan struct{})
			h1 := newServerHarnessWithEndpoint(t, domain, 12401, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				close(entered1)
				<-resume1
				_ = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("one")))
			})
			h2 := newServerHarnessWithEndpoint(t, domain, 12402, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				close(entered2)
				<-resume2
				_ = w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("two")))
			})
			done1, done2 := make(chan struct{}), make(chan struct{})
			go func() { h1.ingest(conGET(t, h1, 70, 1, 0)); close(done1) }()
			go func() { h2.ingest(conGET(t, h2, 71, 2, 0)); close(done2) }()
			<-entered1
			<-entered2
			clock.Advance(time.Second)
			h1.cc.CheckExpirations(clock.Now())
			h2.cc.CheckExpirations(clock.Now())
			close(resume1)
			close(resume2)
			<-done1
			<-done2
			writes1, writes2 := h1.session.writesSnapshot(), h2.session.writesSnapshot()
			require.Len(t, writes1, 2)
			require.Len(t, writes2, 2)
			p1 := conResponsePermit(t, h1, 70)
			p2 := conResponsePermit(t, h2, 71)
			require.True(t, p1.member.ownsActive(1))
			require.True(t, p2.member.ownsActive(1))
			conFeedback(t, h1, writes1[1].mid, firstFeedback)
			require.False(t, p1.member.ownsActive(1))
			require.True(t, p2.member.ownsActive(1))
			h1.cc.qblockClient.mu.Lock()
			firstRoute := h1.cc.qblockClient.server.conByMID[writes1[1].mid]
			h1.cc.qblockClient.mu.Unlock()
			h2.cc.qblockClient.mu.Lock()
			secondRoute := h2.cc.qblockClient.server.conByMID[writes2[1].mid]
			h2.cc.qblockClient.mu.Unlock()
			require.Nil(t, firstRoute)
			require.NotNil(t, secondRoute)
			clock.Advance(2 * time.Second)
			h1.cc.CheckExpirations(clock.Now())
			h2.cc.CheckExpirations(clock.Now())
			require.Len(t, h1.session.writesSnapshot(), 2)
			retried := h2.session.writesSnapshot()
			require.Len(t, retried, 3)
			require.Equal(t, writes2[1], retried[2])
			secondFeedback := map[message.Type]message.Type{message.Acknowledgement: message.Reset, message.Reset: message.Acknowledgement}[firstFeedback]
			conFeedback(t, h2, writes2[1].mid, secondFeedback)
			require.False(t, p2.member.ownsActive(1))
		})
	}
}

func newServerHarnessWithEndpoint(t *testing.T, domain *qblockEndpointDomain, port int, handler HandlerFunc) *serverHarness {
	t.Helper()
	h := &serverHarness{now: time.Unix(100, 0), nextMID: 1}
	h.session = &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(port)}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = 0
	cfg.Handler = handler
	cfg.GetMID = func() int32 { mid := h.nextMID; h.nextMID++; return mid }
	h.cc = NewConnWithOpts(h.session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Now: domain.clock.Now, Endpoint: domain, ScheduleMode: qblockScheduleManual}),
		withQBlockServer(qblockServerConfig{}),
	)
	t.Cleanup(h.session.closeForTest)
	return h
}

func conResponsePermit(t *testing.T, h *serverHarness, requestMID int32) *qblockOrdinaryPermit {
	t.Helper()
	h.cc.qblockClient.mu.Lock()
	defer h.cc.qblockClient.mu.Unlock()
	r := h.cc.qblockClient.server.conRequests[requestMID]
	require.NotNil(t, r)
	require.NotNil(t, r.permit)
	return r.permit
}

func TestQBlockServerCONDeadlineRace(t *testing.T) {
	for i := 0; i < 10; i++ {
		clock := newFakeQBlockClock(time.Unix(100, 0))
		var calls atomic.Int32
		entered := make(chan struct{})
		resume := make(chan struct{})
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			calls.Add(1)
			close(entered)
			<-resume
			w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
		})
		h.cc.qblockClient.now = clock.Now
		done := make(chan struct{})
		go func() { h.ingest(conGET(t, h, 70, 1, 0)); close(done) }()
		<-entered // The CON record and its ACK deadline are now admitted.
		clock.Advance(time.Second)
		start := make(chan struct{})
		tickDone := make(chan struct{})
		releaseDone := make(chan struct{})
		go func() { <-start; h.cc.qblockClient.advanceDue(clock.Now()); close(tickDone) }()
		go func() { <-start; close(resume); close(releaseDone) }()
		close(start)
		<-releaseDone
		<-tickDone
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
