package qblock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type qTestTransport struct {
	*testClient
	jobs            map[string]qBlockTestJob
	sent            []*pool.Message
	delayQBlockHook func(string)
}

type testClient struct {
	p *pool.Pool
}

func newTestClient() *testClient {
	return &testClient{p: pool.New(100, 1024)}
}

func (c *testClient) AcquireMessage(ctx context.Context) *pool.Message {
	return c.p.AcquireMessage(ctx)
}

func (c *testClient) ReleaseMessage(m *pool.Message) {
	c.p.ReleaseMessage(m)
}

type qBlockTestJob struct {
	deadline time.Time
	run      func() error
}

const (
	q2TestTokenAlpha = "alpha-token"
	q2TestTokenBeta  = "beta-token"
)

func (c *qTestTransport) OpenQBlockExchange(context.Context, message.Token) (Exchange, error) {
	return nil, errors.New("not used")
}

func (c *qTestTransport) ScheduleQBlock(key string, deadline time.Time, run func() error) error {
	if old, ok := c.jobs[key]; ok && old.deadline.Before(deadline) {
		return nil
	}
	c.jobs[key] = qBlockTestJob{deadline, run}
	return nil
}
func (c *qTestTransport) CancelQBlock(key string) { delete(c.jobs, key) }
func (c *qTestTransport) DelayQBlock(key string, deadline time.Time) {
	if job, ok := c.jobs[key]; ok && job.deadline.Before(deadline) {
		job.deadline = deadline
		c.jobs[key] = job
	}
	if c.delayQBlockHook != nil {
		c.delayQBlockHook(key)
	}
}

func (c *qTestTransport) SendQBlock(msg *pool.Message) error {
	if msg.Context() == nil {
		return errors.New("scheduled Q-Block response has nil context")
	}
	cloned := pool.NewMessage(context.Background())
	if err := msg.Clone(cloned); err != nil {
		return err
	}
	c.sent = append(c.sent, cloned)
	return nil
}

func qTestRequest(t *testing.T, token string, controls ...struct {
	num  int64
	more bool
},
) *pool.Message {
	t.Helper()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetToken([]byte(token))
	require.NoError(t, req.SetPath("/q"))
	for _, control := range controls {
		value, err := EncodeQBlockOption(SZX16, control.num, control.more)
		require.NoError(t, err)
		req.AddOptionUint32(message.QBlock2, value)
	}
	return req
}

func TestQBlock2RequestedSets(t *testing.T) {
	type control = struct {
		num  int64
		more bool
	}
	for _, tc := range []struct {
		name     string
		controls []control
		want     []int64
		whole    bool
		invalid  bool
	}{
		{"body", []control{{0, true}}, []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, true, false},
		{"continue", []control{{10, true}}, []int64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, true, false},
		{"remainder", []control{{13, true}}, []int64{13, 14, 15, 16, 17, 18, 19}, false, false},
		{"overlap", []control{{6, true}, {8, false}}, []int64{6, 7, 8, 9}, false, false},
		{"single", []control{{13, false}}, []int64{13}, false, false},
		{"duplicate", []control{{3, false}, {3, false}}, nil, false, true},
		{"descending", []control{{3, false}, {1, false}}, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nums, whole, err := New(newTestClient(), nil).q2RequestedBlocks(qTestRequest(t, "a", tc.controls...), SZX16, 32)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, nums)
			require.Equal(t, tc.whole, whole)
		})
	}
}

func TestQBlock2BurstPacingRecoveryAndChangedToken(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	body := bytes.Repeat([]byte{7}, 32*16)
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(body)))
		w.Message().SetObserve(2)
	}
	invoke := func(req *pool.Message) *pool.Message {
		w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
		require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
		return w.Message()
	}
	type control = struct {
		num  int64
		more bool
	}
	first := invoke(qTestRequest(t, "first", control{0, true}))
	etag, err := first.GetOptionBytes(message.ETag)
	require.NoError(t, err)
	require.Len(t, cc.jobs, 1)
	runJob := func() time.Time {
		require.Len(t, cc.jobs, 1)
		for key, job := range cc.jobs {
			delete(cc.jobs, key)
			require.NoError(t, job.run())
			return job.deadline
		}
		return time.Time{}
	}
	runJob()
	require.Len(t, cc.sent, 9)
	for _, job := range cc.jobs {
		require.GreaterOrEqual(t, time.Until(job.deadline), 1900*time.Millisecond)
		require.LessOrEqual(t, time.Until(job.deadline), 3*time.Second)
	}
	cc.sent = nil
	second := invoke(qTestRequest(t, "changed", control{10, true}))
	require.Equal(t, message.Token("changed"), second.Token())
	require.Less(t, time.Until(runJob()), 100*time.Millisecond)
	require.Len(t, cc.sent, 9)
	for i, msg := range cc.sent {
		value, err := msg.GetOptionUint32(message.QBlock2)
		require.NoError(t, err)
		_, num, _, err := DecodeQBlockOption(value)
		require.NoError(t, err)
		require.Equal(t, int64(i+11), num)
		require.Equal(t, message.Token("changed"), msg.Token())
		gotETag, err := msg.GetOptionBytes(message.ETag)
		require.NoError(t, err)
		require.Equal(t, etag, gotETag)
		require.True(t, msg.HasOption(message.Observe))
	}
	cc.jobs = make(map[string]qBlockTestJob)
	cc.sent = nil
	recovery := invoke(qTestRequest(t, "recover", control{4, true}, control{6, false}))
	require.False(t, recovery.HasOption(message.Observe))
	runJob()
	require.Len(t, cc.sent, 5)
	for i, msg := range cc.sent {
		value, err := msg.GetOptionUint32(message.QBlock2)
		require.NoError(t, err)
		_, num, _, err := DecodeQBlockOption(value)
		require.NoError(t, err)
		require.Equal(t, int64(i+5), num)
		require.False(t, msg.HasOption(message.Observe))
	}
	cc.sent = nil
	invoke(qTestRequest(t, "spanning", control{4, true}, control{13, true}))
	runJob()
	require.Len(t, cc.sent, 5, "finish the first recovery set before the next set")
	for _, job := range cc.jobs {
		require.GreaterOrEqual(t, time.Until(job.deadline), 1900*time.Millisecond)
	}
	runJob()
	require.Len(t, cc.sent, 12, "recover both disjoint payload sets")
	for i, msg := range cc.sent[5:] {
		value, err := msg.GetOptionUint32(message.QBlock2)
		require.NoError(t, err)
		_, num, _, err := DecodeQBlockOption(value)
		require.NoError(t, err)
		require.Equal(t, int64(i+13), num)
	}
	require.Equal(t, 1, calls, "recovery must reuse the immutable response")
}

func TestQBlockTransferLifecycleBudget(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := newTestClient()
	b := New(cc, func(error) {})
	response := cc.AcquireMessage(context.Background())
	response.SetCode(codes.Content)
	response.SetBody(bytes.NewReader(bytes.Repeat([]byte{1}, 1024)))
	require.NoError(t, b.cacheQBlock2Response(response.Context(), "body", "identity", response, message.NonConfirmable))
	require.Equal(t, bytesBefore+1024, qRetainedBytes.Load())
	require.Equal(t, transfersBefore+1, qRetainedTransfers.Load())
	b.CheckExpirations(time.Now().Add(61 * time.Second))
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
	require.NoError(t, b.cacheQBlock2Response(response.Context(), "body", "identity", response, message.NonConfirmable))
	b.CloseQBlockTransfers()
	b.CloseQBlockTransfers()
	require.ErrorIs(t, b.cacheQBlock2Response(response.Context(), "body", "identity", response, message.NonConfirmable), context.Canceled)
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlock2NewRequestWithPayloadIsNotResumed(t *testing.T) {
	type control = struct {
		num  int64
		more bool
	}
	for _, code := range []codes.Code{codes.POST, codes.PUT, codes.FETCH, codes.PATCH, codes.IPATCH} {
		for _, tc := range []struct {
			name string
			more bool
		}{{"whole-body", true}, {"first-block", false}} {
			t.Run(code.String()+"/"+tc.name, func(t *testing.T) {
				cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
				b := New(cc, func(error) {})
				defer b.CloseQBlockTransfers()
				calls := 0
				handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
					calls++
					payload, err := io.ReadAll(r.Body())
					require.NoError(t, err)
					require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 64))))
				}
				invoke := func(token, payload string, more bool) *pool.Message {
					req := qTestRequest(t, token, control{0, more})
					req.SetCode(code)
					req.SetBody(bytes.NewReader([]byte(payload)))
					w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
					require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
					cc.ReleaseMessage(req)
					return w.Message()
				}
				first := invoke(q2TestTokenAlpha, "A", true)
				cc.ReleaseMessage(first)
				second := invoke(q2TestTokenBeta, "B", tc.more)
				defer cc.ReleaseMessage(second)
				require.Equal(t, 2, calls, "a new request must reach the handler")
				body, err := io.ReadAll(second.Body())
				require.NoError(t, err)
				require.Equal(t, bytes.Repeat([]byte("B"), 16), body)

				for _, expected := range []struct {
					token   string
					payload byte
				}{{q2TestTokenAlpha, 'A'}, {q2TestTokenBeta, 'B'}} {
					recovery := qTestRequest(t, expected.token, control{1, false})
					recovery.SetCode(code)
					w := responsewriter.New(cc.AcquireMessage(recovery.Context()), cc)
					require.NoError(t, b.handleQBlock2(w, recovery, SZX16, nil, handler))
					cc.ReleaseMessage(recovery)
					defer cc.ReleaseMessage(w.Message())
					require.Equal(t, 2, calls, "recovery must reuse its original cached response")
					recoveredBody, err := io.ReadAll(w.Message().Body())
					require.NoError(t, err)
					require.Equal(t, bytes.Repeat([]byte{expected.payload}, 16), recoveredBody)
				}
			})
		}
	}
}

func TestQBlock2NewRequestWithSameTokenReplacesCachedResponse(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		calls++
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	invoke := func(payload string, num int64, messageID int32) *pool.Message {
		req := qTestRequest(t, "same", struct {
			num  int64
			more bool
		}{num, false})
		req.SetMessageID(messageID)
		req.SetCode(codes.POST)
		if num == 0 {
			req.SetBody(bytes.NewReader([]byte(payload)))
		}
		w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
		require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
		cc.ReleaseMessage(req)
		return w.Message()
	}

	first := invoke("A", 0, 1)
	cc.ReleaseMessage(first)
	second := invoke("B", 0, 2)
	defer cc.ReleaseMessage(second)
	require.Equal(t, 2, calls)
	body, err := io.ReadAll(second.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("B"), 16), body)

	recovery := invoke("", 1, 3)
	defer cc.ReleaseMessage(recovery)
	require.Equal(t, 2, calls, "same-token continuation must use the replacement response")
	recoveredBody, err := io.ReadAll(recovery.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("B"), 16), recoveredBody)
}

func TestQBlock2RetransmittedNewRequestReusesCachedResponse(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		calls++
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	send := func() *pool.Message {
		req := qTestRequest(t, "retry", struct {
			num  int64
			more bool
		}{0, false})
		req.SetMessageID(42)
		req.SetType(message.Confirmable)
		req.SetCode(codes.POST)
		req.SetBody(bytes.NewReader([]byte("A")))
		response := cc.AcquireMessage(req.Context())
		response.SetType(message.Confirmable)
		response.SetMessageID(req.MessageID())
		w := responsewriter.New(response, cc)
		require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
		cc.ReleaseMessage(req)
		return w.Message()
	}

	first := send()
	defer cc.ReleaseMessage(first)
	retransmission := send()
	defer cc.ReleaseMessage(retransmission)
	require.Equal(t, 1, calls, "a retransmission must not repeat application effects")
	require.Equal(t, int32(42), first.MessageID())
	require.Equal(t, message.Confirmable, first.Type())
	require.Equal(t, first.MessageID(), retransmission.MessageID())
	require.Equal(t, first.Type(), retransmission.Type())
	body, err := io.ReadAll(retransmission.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("A"), 16), body)
	require.Len(t, b.q2Senders, 1)
}

func TestQBlock2NegotiatedSZXDifferentMessageIDsAreNewRequests(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	handlerCalls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		handlerCalls++
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	send := func(messageID int32, payload string) *pool.Message {
		req := qTestRequest(t, "negotiated", struct {
			num  int64
			more bool
		}{0, false})
		req.SetMessageID(messageID)
		req.SetCode(codes.POST)
		req.SetBody(bytes.NewReader([]byte(payload)))
		value, err := EncodeQBlockOption(SZX32, 0, false)
		require.NoError(t, err)
		req.SetOptionUint32(message.QBlock2, value)
		w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
		require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
		cc.ReleaseMessage(req)
		return w.Message()
	}

	first := send(21, "A")
	cc.ReleaseMessage(first)
	second := send(22, "B")
	defer cc.ReleaseMessage(second)
	require.Equal(t, 2, handlerCalls, "distinct request MIDs must invoke the handler independently")
	body, err := io.ReadAll(second.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("B"), 16), body)
}

func TestQBlock2NegotiatedSZXRetransmissionReusesCachedResponse(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	handlerCalls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		handlerCalls++
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	send := func() *pool.Message {
		req := qTestRequest(t, "negotiated-retry", struct {
			num  int64
			more bool
		}{0, false})
		req.SetMessageID(23)
		req.SetCode(codes.POST)
		req.SetBody(bytes.NewReader([]byte("A")))
		value, err := EncodeQBlockOption(SZX32, 0, false)
		require.NoError(t, err)
		req.SetOptionUint32(message.QBlock2, value)
		w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
		require.NoError(t, b.handleQBlock2(w, req, SZX16, nil, handler))
		cc.ReleaseMessage(req)
		return w.Message()
	}

	first := send()
	defer cc.ReleaseMessage(first)
	retransmission := send()
	defer cc.ReleaseMessage(retransmission)
	require.Equal(t, 1, handlerCalls, "same-MID retransmission must reuse the cached response")
	body, err := io.ReadAll(retransmission.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("A"), 16), body)
}

func TestQBlock2NewRequestMessageIDZeroDoesNotMatchRequestlessSender(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()

	request := qTestRequest(t, "shared-token", struct {
		num  int64
		more bool
	}{0, false})
	request.SetCode(codes.POST)
	request.SetType(message.Confirmable)
	key := q2TransferKey(request, nil)
	identity := qTransferKey(request, nil, nil, true)
	response := cc.AcquireMessage(request.Context())
	response.SetCode(codes.Content)
	response.SetType(message.Confirmable)
	response.SetMessageID(0)
	response.SetBody(bytes.NewReader(bytes.Repeat([]byte("old"), 16)))
	_, err := b.cacheQBlock2Notification(request.Context(), key, identity, response,
		message.Confirmable, nil, false, 0)
	require.NoError(t, err)
	cc.ReleaseMessage(response)

	handlerCalls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
		handlerCalls++
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat([]byte("N"), 32))))
	}
	request.SetBody(bytes.NewReader([]byte("payload")))
	w := responsewriter.New(cc.AcquireMessage(request.Context()), cc)
	require.NoError(t, b.handleQBlock2(w, request, SZX16, nil, handler))
	defer cc.ReleaseMessage(w.Message())

	require.Equal(t, 1, handlerCalls, "MID zero on a requestless sender must not look like a retransmission")
	body, err := io.ReadAll(w.Message().Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte("N"), 16), body)
}

func TestQBlock2ChangedTokenRecoveryRejectsAmbiguousTransfers(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		calls++
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	send := func(token, payload string, num int64) (*pool.Message, error) {
		req := qTestRequest(t, token, struct {
			num  int64
			more bool
		}{num, false})
		req.SetCode(codes.POST)
		if payload != "" {
			req.SetBody(bytes.NewReader([]byte(payload)))
		}
		w := responsewriter.New(cc.AcquireMessage(req.Context()), cc)
		err := b.handleQBlock2(w, req, SZX16, nil, handler)
		cc.ReleaseMessage(req)
		return w.Message(), err
	}

	first, err := send(q2TestTokenAlpha, "A", 0)
	require.NoError(t, err)
	cc.ReleaseMessage(first)
	second, err := send(q2TestTokenBeta, "B", 0)
	require.NoError(t, err)
	cc.ReleaseMessage(second)
	require.Equal(t, 2, calls)

	keyFor := func(token string) string {
		req := qTestRequest(t, token)
		req.SetCode(codes.POST)
		key := q2TransferKey(req, nil)
		cc.ReleaseMessage(req)
		return key
	}
	alphaKey := keyFor(q2TestTokenAlpha)
	betaKey := keyFor(q2TestTokenBeta)
	alphaSender, betaSender := b.q2Senders[alphaKey], b.q2Senders[betaKey]
	require.NotNil(t, alphaSender)
	require.NotNil(t, betaSender)

	ambiguous := qTestRequest(t, "fresh-token", struct {
		num  int64
		more bool
	}{1, false})
	ambiguous.SetCode(codes.POST)
	ambiguousResponse := responsewriter.New(cc.AcquireMessage(ambiguous.Context()), cc)
	b.Handle(ambiguousResponse, ambiguous, SZX16, nil, handler)
	cc.ReleaseMessage(ambiguous)
	defer cc.ReleaseMessage(ambiguousResponse.Message())
	require.Equal(t, codes.BadRequest, ambiguousResponse.Message().Code())
	require.Len(t, b.q2Senders, 2)
	require.Same(t, alphaSender, b.q2Senders[alphaKey])
	require.Same(t, betaSender, b.q2Senders[betaKey])

	for _, expected := range []struct {
		token   string
		payload byte
	}{{q2TestTokenAlpha, 'A'}, {q2TestTokenBeta, 'B'}} {
		recovery, err := send(expected.token, "", 1)
		require.NoError(t, err)
		body, err := io.ReadAll(recovery.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{expected.payload}, 16), body)
		cc.ReleaseMessage(recovery)
	}
	require.Equal(t, 2, calls, "recovery must not invoke the handler")
}

func TestQBlock2ConcurrentNewRequestsKeepSeparateSenders(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	var calls atomic.Int32
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		calls.Add(1)
		payload, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(bytes.Repeat(payload, 32))))
	}
	type result struct {
		token   string
		payload string
		msg     *pool.Message
		err     error
	}
	makeRequest := func(request struct {
		token   string
		payload string
	},
	) *pool.Message {
		req := qTestRequest(t, request.token, struct {
			num  int64
			more bool
		}{0, false})
		req.SetCode(codes.POST)
		req.SetBody(bytes.NewReader([]byte(request.payload)))
		return req
	}
	alphaRequest := makeRequest(struct {
		token   string
		payload string
	}{q2TestTokenAlpha, "A"})
	defer cc.ReleaseMessage(alphaRequest)
	alphaKey := "q2:" + q2TransferKey(alphaRequest, nil)
	alphaPaused, releaseAlpha := make(chan struct{}), make(chan struct{})
	var pauseAlphaOnce sync.Once
	var releaseAlphaOnce sync.Once
	defer releaseAlphaOnce.Do(func() { close(releaseAlpha) })
	cc.delayQBlockHook = func(key string) {
		if key == alphaKey {
			pauseAlphaOnce.Do(func() {
				close(alphaPaused)
				<-releaseAlpha
			})
		}
	}
	alphaResult := make(chan result, 1)
	go func() {
		w := responsewriter.New(cc.AcquireMessage(alphaRequest.Context()), cc)
		err := b.handleQBlock2(w, alphaRequest, SZX16, nil, handler)
		alphaResult <- result{q2TestTokenAlpha, "A", w.Message(), err}
	}()
	select {
	case <-alphaPaused:
	case <-time.After(time.Second):
		releaseAlphaOnce.Do(func() { close(releaseAlpha) })
		<-alphaResult
		t.Fatal("alpha did not pause after its sender was cached")
	}

	betaRequest := makeRequest(struct {
		token   string
		payload string
	}{q2TestTokenBeta, "B"})
	betaWriter := responsewriter.New(cc.AcquireMessage(betaRequest.Context()), cc)
	betaErr := b.handleQBlock2(betaWriter, betaRequest, SZX16, nil, handler)
	cc.ReleaseMessage(betaRequest)
	require.NoError(t, betaErr)
	require.Len(t, b.q2Senders, 2, "beta must not remove alpha's already-cached sender")
	releaseAlphaOnce.Do(func() { close(releaseAlpha) })

	results := []result{{q2TestTokenBeta, "B", betaWriter.Message(), nil}, <-alphaResult}
	require.Len(t, b.q2Senders, 2)
	for _, result := range results {
		require.NoError(t, result.err)
		body, err := io.ReadAll(result.msg.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte(result.payload), 16), body)
		cc.ReleaseMessage(result.msg)
	}
	require.EqualValues(t, 2, calls.Load())

	for _, request := range []struct {
		token   string
		payload string
	}{{q2TestTokenAlpha, "A"}, {q2TestTokenBeta, "B"}} {
		recovery := qTestRequest(t, request.token, struct {
			num  int64
			more bool
		}{1, false})
		recovery.SetCode(codes.POST)
		w := responsewriter.New(cc.AcquireMessage(recovery.Context()), cc)
		require.NoError(t, b.handleQBlock2(w, recovery, SZX16, nil, handler))
		cc.ReleaseMessage(recovery)
		body, err := io.ReadAll(w.Message().Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte(request.payload), 16), body)
		cc.ReleaseMessage(w.Message())
	}
	require.EqualValues(t, 2, calls.Load())
}

func TestQBlockEmptyBodies(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(err error) { t.Error(err) })
	defer b.CloseQBlockTransfers()
	type control = struct {
		num  int64
		more bool
	}
	request := qTestRequest(t, "empty", control{0, true})
	w := responsewriter.New(cc.AcquireMessage(request.Context()), cc)
	b.Handle(w, request, SZX16, nil, func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, nil))
	})
	size, err := w.Message().GetOptionUint32(message.Size2)
	require.NoError(t, err)
	require.Zero(t, size)
	value, err := w.Message().GetOptionUint32(message.QBlock2)
	require.NoError(t, err)
	_, num, more, err := DecodeQBlockOption(value)
	require.NoError(t, err)
	require.Zero(t, num)
	require.False(t, more)
	require.Empty(t, cc.jobs)

	request.SetCode(codes.PUT)
	request.SetOptionUint32(message.QBlock1, 0)
	request.SetOptionUint32(message.Size1, 0)
	require.NoError(t, request.SetRequestTag([]byte("empty")))
	w = responsewriter.New(cc.AcquireMessage(request.Context()), cc)
	calls := 0
	b.Handle(w, request, SZX16, nil, func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
		calls++
		size, err := r.BodySize()
		require.NoError(t, err)
		require.Zero(t, size)
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, nil))
	})
	require.Equal(t, codes.Changed, w.Message().Code())
	require.Equal(t, 1, calls)
}

type qOversizedSource struct{}

func (qOversizedSource) Read([]byte) (int, error) { panic("oversized source must not be read") }
func (qOversizedSource) Seek(_ int64, whence int) (int64, error) {
	if whence == 2 {
		return q1DefaultMaxBodySize + 1, nil
	}
	return 0, nil
}

func TestQBlockOversizedResponseIsBoundedAndFinalErrorReplayed(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.PUT)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte("first"))
	require.NoError(t, request.SetPath("/q"))
	require.NoError(t, request.SetRequestTag([]byte("body")))
	request.SetOptionUint32(message.QBlock1, 0)
	request.SetOptionUint32(message.QBlock2, 8)
	request.SetOptionUint32(message.Size1, 1)
	request.SetBody(bytes.NewReader([]byte{7}))
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.AppOctets, qOversizedSource{}))
	}
	for _, token := range []string{"first", "retry"} {
		request.SetToken([]byte(token))
		_, err := request.Body().Seek(0, 0)
		require.NoError(t, err)
		w := responsewriter.New(cc.AcquireMessage(request.Context()), cc)
		b.Handle(w, request, SZX16, nil, handler)
		require.Equal(t, codes.RequestEntityTooLarge, w.Message().Code())
		require.Nil(t, w.Message().Body())
	}
	require.Equal(t, 1, calls)
	require.Zero(t, b.q1ReceiveBytes)
	require.Empty(t, b.q2Senders)
}

func TestQBlockConcurrentDuplicateCannotPoisonFinalResponse(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, func(error) {})
	defer b.CloseQBlockTransfers()
	makeRequest := func(token string, sequence uint64, size uint32) *pool.Message {
		req := pool.NewMessage(context.Background())
		req.SetCode(codes.PUT)
		req.SetType(message.NonConfirmable)
		req.SetToken([]byte(token))
		req.SetSequence(sequence)
		require.NoError(t, req.SetRequestTag([]byte("body")))
		req.SetOptionUint32(message.QBlock1, 0)
		req.SetOptionUint32(message.Size1, size)
		req.SetBody(bytes.NewReader([]byte{7}))
		return req
	}
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	first := makeRequest("first", 1, 1)
	w := responsewriter.New(cc.AcquireMessage(first.Context()), cc)
	go func() {
		defer close(done)
		b.Handle(w, first, SZX16, nil, func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
			close(started)
			<-finish
			if err := w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader([]byte{7})); err != nil {
				t.Error(err)
			}
		})
	}()
	<-started
	bad := makeRequest("retry", 2, 2)
	badResponse := responsewriter.New(cc.AcquireMessage(bad.Context()), cc)
	b.Handle(badResponse, bad, SZX16, nil, func(*responsewriter.ResponseWriter[*qTestTransport], *pool.Message) {
		t.Error("duplicate invoked the handler")
	})
	require.Equal(t, codes.BadRequest, badResponse.Message().Code())
	close(finish)
	<-done
	require.Equal(t, codes.Changed, w.Message().Code())
	valid := makeRequest("valid", 3, 1)
	replay := responsewriter.New(cc.AcquireMessage(valid.Context()), cc)
	b.Handle(replay, valid, SZX16, nil, func(*responsewriter.ResponseWriter[*qTestTransport], *pool.Message) {
		t.Error("completed body invoked the handler")
	})
	require.Equal(t, codes.Changed, replay.Message().Code())
}
