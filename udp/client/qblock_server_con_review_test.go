package client

import (
	"bytes"
	"errors"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestQBlockServerCONReviewInvalidETag(t *testing.T) {
	for _, etag := range [][]byte{nil, make([]byte, 9)} {
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")), message.Option{ID: message.ETag, Value: etag})
		})
		h.ingest(conGET(t, h, 70, 1, 0))
		writes := h.session.writesSnapshot()
		require.Len(t, writes, 1)
		require.Equal(t, codes.InternalServerError, writes[0].code)
	}
}
func TestQBlockServerCONReviewMetadataAggregate(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxMetadataBytes: 400}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")), message.Option{ID: message.LocationPath, Value: bytes.Repeat([]byte{'v'}, 100)})
	})
	h.ingest(conGET(t, h, 70, 1, 0))
	h.ingest(conGET(t, h, 71, 2, 0))
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 2)
	require.NotEqual(t, codes.Content, writes[1].code)
}
func TestQBlockServerCONReviewExpiredDuplicate(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
	})
	h.ingest(conGET(t, h, 70, 1, 0))
	h.now = h.now.Add(ExchangeLifetime)
	h.ingest(conGET(t, h, 70, 1, 0))
	require.Len(t, h.session.writesSnapshot(), 1)
}
func TestQBlockServerCONReviewErrorsOutsideGate(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
	})
	h.session.writeErr = errors.New("write failed")
	outside := false
	h.cc.errors = func(error) {
		if h.cc.qblockClient.actionMu.TryLock() {
			outside = true
			h.cc.qblockClient.actionMu.Unlock()
		}
	}
	h.ingest(conGET(t, h, 70, 1, 0))
	require.True(t, outside, "user Errors callback must be outside action gate")
}
func TestQBlockServerCONReviewMIDCollision(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.advance(time.Second)
	// GetMessageID increments this counter, so its next candidate is101.
	h.cc.msgID.Store(100)
	existing := &midElement{handler: func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {}}
	h.cc.midHandlerContainer.Store(101, existing)
	close(resume)
	<-done
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 2)
	require.NotEqualValues(t, 101, writes[1].mid)
	got, ok := h.cc.midHandlerContainer.Load(101)
	require.True(t, ok)
	require.Same(t, existing, got)
	conFeedback(t, h, writes[1].mid, message.Acknowledgement)
}
func TestQBlockServerCONReviewTokenFeedback(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	clock := newFakeQBlockClock(h.now)
	domain := newQBlockEndpointDomain(clock, 65536, 10, 10)
	h.session.remoteAddr = endpointPeer(123)
	member, err := domain.attach(h.cc.RemoteAddr(), make(chan struct{}, 1), clock.Now())
	require.NoError(t, err)
	h.cc.qblockClient.endpoint = member
	h.advance(time.Second)
	close(resume)
	<-done
	msg := h.cc.AcquireMessage(h.cc.Context())
	defer h.cc.ReleaseMessage(msg)
	msg.SetType(message.NonConfirmable)
	msg.SetCode(codes.Content)
	msg.SetToken([]byte{1})
	msg.SetMessageID(999)
	require.False(t, h.cc.acceptOrdinaryResponse(msg), "a response token cannot acknowledge a server CON")
	h.advance(2 * time.Second)
	require.Len(t, h.session.writesSnapshot(), 3)
}

func TestQBlockServerCONReviewApplicationErrorOffset(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		w.SetResponse(codes.Unauthorized, message.TextPlain, bytes.NewReader([]byte("denied")))
	})
	h.ingest(conGET(t, h, 70, 1, 16))
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.Unauthorized, writes[0].code)
	require.Equal(t, "denied", string(writes[0].payload))
	require.False(t, writes[0].options.HasOption(message.QBlock2))
}
func TestQBlockServerCONReviewFitSZX(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'x'}, 200)))
	})
	h.cc.blockwiseSZX = 2
	h.cc.qblockClient.datagramLimit = 64
	h.ingest(conGET(t, h, 70, 1, 18)) // NUM1/SZX2 asks byte64.
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.Content, writes[0].code)
	require.EqualValues(t, 41, writes[0].block) // NUM2/M1/SZX1
	require.Len(t, writes[0].payload, 32)
}
func TestQBlockServerCONReviewReverseMIDCollision(t *testing.T) {
	h, resume, done := delayedCONHarness(t)
	h.advance(time.Second)
	close(resume)
	<-done
	writes := h.session.writesSnapshot()
	mid := writes[1].mid
	req := h.cc.AcquireMessage(h.cc.Context())
	defer h.cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetMessageID(mid)
	cleanup, err := h.cc.prepareWriteMessage(req, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	if cleanup != nil {
		cleanup()
	}
	require.Error(t, err, "ordinary writer must reject an owned server response MID")
	conFeedback(t, h, mid, message.Acknowledgement)
}
