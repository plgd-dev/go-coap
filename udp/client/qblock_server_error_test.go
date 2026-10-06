package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func errorServerRequest(t *testing.T, h *serverHarness, method codes.Code) *pool.Message {
	t.Helper()
	req := h.q1(t, 1, 0, false, 4, "body")
	if method == codes.GET {
		h.cc.ReleaseMessage(req)
		req = h.control(t, 1, 0, true, "tag-a")
	}
	req.SetCode(method)
	return req
}

// Application errors must be ordinary terminal responses: Q-bearing errors
// are intentionally invalid first fragments and previously left Do pending.
func TestQBlockNONApplicationError(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.PUT} {
		for _, status := range []codes.Code{codes.NotFound, codes.InternalServerError} {
			for _, body := range []string{"", "missing"} {
				t.Run(method.String()+"/"+status.String()+"/"+body, func(t *testing.T) {
					calls := 0
					h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
						calls++
						require.NoError(t, w.SetResponse(status, message.TextPlain, bytes.NewReader([]byte(body))))
					})
					h.ingest(errorServerRequest(t, h, method))
					writes := h.session.writesSnapshot()
					require.Len(t, writes, 1)
					wire := writes[0]
					require.Equal(t, status, wire.code)
					require.Equal(t, message.NonConfirmable, wire.typ)
					require.False(t, wire.options.HasOption(message.QBlock2))
					require.False(t, wire.options.HasOption(message.Size2))
					require.Equal(t, body, string(wire.payload))
					require.Zero(t, h.snapshot().active)
					h.ingest(errorServerRequest(t, h, method))
					require.Equal(t, 1, calls)

					session := &qblockTestSession{ctx: context.Background()}
					cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
					req := newPrivateQBlockClientGET(t, cc, message.Token{0x93})
					defer cc.ReleaseMessage(req)
					req.SetCode(method)
					if method != codes.GET {
						req.SetBody(bytes.NewReader([]byte("body")))
					}
					received := 0
					failures := 0
					cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], m *pool.Message) {
						received++
						defer cc.ReleaseMessage(m)
						require.Equal(t, status, m.Code())
						b, e := m.ReadBody()
						require.NoError(t, e)
						require.Equal(t, body, string(b))
					})
					prepared, e := cc.qblockClient.prepare(req, func(error) { failures++ })
					require.NoError(t, e)
					require.True(t, prepared.Prepared)
					token := req.Token()
					if method != codes.GET {
						token = session.writesSnapshot()[0].token
					}
					deliver := func() {
						reply := cc.AcquireMessage(context.Background())
						reply.SetType(wire.typ)
						reply.SetCode(wire.code)
						reply.SetMessageID(wire.mid)
						reply.SetToken(token)
						reply.ResetOptionsTo(wire.options)
						reply.SetBody(bytes.NewReader(wire.payload))
						cc.ProcessReceivedMessageWithHandler(reply, cc.handleReq)
					}
					deliver()
					require.Equal(t, 1, received)
					require.Zero(t, failures)
					requireQBlockClientEmpty(t, cc)
					require.Empty(t, cc.qblockClient.pendingGETByMID)
					require.Empty(t, cc.qblockClient.workQueue.slots)
					deliver()
					require.Equal(t, 1, received)
					require.Zero(t, failures)
					h.advance(2 * h.cc.qblockClient.managerConfig.Transfer.Lifetime)
					require.Equal(t, serverSnapshot{}, h.snapshot())
				})
			}
		}
	}
}

func TestQBlockNONApplicationErrorBounds(t *testing.T) {
	for _, kind := range []string{"oversize_body", "oversize_options", "no_response", "expired"} {
		t.Run(kind, func(t *testing.T) {
			var h *serverHarness
			h = newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				resp := w.Message()
				resp.SetCode(codes.NotFound)
				resp.SetContentFormat(message.TextPlain)
				resp.SetBody(bytes.NewReader([]byte("missing")))
				if kind == "oversize_body" {
					resp.SetBody(bytes.NewReader(bytes.Repeat([]byte{'x'}, 256)))
				}
				if kind == "oversize_options" {
					resp.SetOptionBytes(message.LocationPath, bytes.Repeat([]byte{'x'}, 256))
				}
				if kind == "expired" {
					h.advance(h.cc.qblockClient.managerConfig.Transfer.Lifetime)
				}
			})
			h.cc.qblockClient.datagramLimit = 64
			req := errorServerRequest(t, h, codes.GET)
			if kind == "no_response" {
				req.SetOptionUint32(message.NoResponse, 8)
			}
			h.ingest(req)
			writes := h.session.writesSnapshot()
			if kind == "no_response" || kind == "expired" {
				require.Empty(t, writes)
			} else {
				require.Len(t, writes, 1)
				require.Equal(t, codes.NotFound, writes[0].code)
				require.Empty(t, writes[0].payload)
				require.Empty(t, writes[0].options)
			}
			h.advance(2 * h.cc.qblockClient.managerConfig.Transfer.Lifetime)
			require.Equal(t, serverSnapshot{}, h.snapshot())
		})
	}
}

// Rejecting missing required metadata must not admit a partial body or steal
// ownership from a different live transfer sharing the packet token.
func TestQBlockNONOversizedQ1Size(t *testing.T) {
	for _, method := range []codes.Code{codes.POST, codes.PUT} {
		t.Run(method.String(), func(t *testing.T) {
			mc := qblock.DefaultManagerConfig()
			mc.Transfer.MaxBodySize = 16
			calls := 0
			h := newServerHarness(t, mc, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				calls++
				_ = w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("handled")))
			})
			req := h.q1(t, 1, 0, true, 32, "0123456789abcdef")
			req.SetCode(method)

			h.ingest(req)

			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			wire := writes[0]
			require.Equal(t, codes.RequestEntityTooLarge, wire.code)
			require.Equal(t, message.NonConfirmable, wire.typ)
			require.Equal(t, message.Token{1}, wire.token)
			require.Empty(t, wire.payload)
			require.Len(t, wire.options, 1)
			require.False(t, wire.options.HasOption(message.QBlock1))
			require.False(t, wire.options.HasOption(message.QBlock2))
			maxSize, err := wire.options.GetUint32(message.Size1)
			require.NoError(t, err)
			require.Equal(t, uint32(16), maxSize)

			response := h.cc.AcquireMessage(context.Background())
			defer h.cc.ReleaseMessage(response)
			response.SetType(wire.typ)
			response.SetCode(wire.code)
			response.SetMessageID(wire.mid)
			response.SetToken(wire.token)
			response.ResetOptionsTo(wire.options)
			wireSize, err := qblockDatagramSize(response)
			require.NoError(t, err)
			require.LessOrEqual(t, wireSize, uint64(h.cc.qblockClient.datagramLimit))

			require.Zero(t, calls)
			require.Equal(t, serverSnapshot{}, h.snapshot())
		})
	}
}

func TestQBlockNONOversizedQ1SizeNoResponse(t *testing.T) {
	mc := qblock.DefaultManagerConfig()
	mc.Transfer.MaxBodySize = 16
	h := newServerHarness(t, mc, qblockServerConfig{}, nil)
	req := h.q1(t, 1, 0, true, 32, "0123456789abcdef")
	req.SetOptionUint32(message.NoResponse, 8)

	h.ingest(req)

	require.Empty(t, h.session.writesSnapshot())
	require.Equal(t, serverSnapshot{}, h.snapshot())
}

func TestQBlockNONMissingQ1Metadata(t *testing.T) {
	for _, method := range []codes.Code{codes.POST, codes.PUT} {
		for _, missing := range []message.OptionID{message.RequestTag, message.Size1} {
			for _, live := range []bool{false, true} {
				t.Run(method.String()+"/"+missing.String()+"/"+map[bool]string{false: "fresh", true: "live"}[live], func(t *testing.T) {
					calls := 0
					h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) { calls++ })
					request := func(n uint32, more bool) *pool.Message {
						m := h.q1(t, 1, n, more, 32, "0123456789abcdef")
						m.SetCode(method)
						return m
					}
					if live {
						h.ingest(request(0, true))
					}
					before := h.snapshot()
					req := request(1, false)
					req.Remove(missing)
					h.ingest(req)
					writes := h.session.writesSnapshot()
					require.Len(t, writes, 1)
					require.Equal(t, codes.BadRequest, writes[0].code)
					require.Equal(t, message.Token{1}, writes[0].token)
					require.Equal(t, message.NonConfirmable, writes[0].typ)
					require.Empty(t, writes[0].payload)
					require.False(t, writes[0].options.HasOption(message.QBlock1))
					require.False(t, writes[0].options.HasOption(message.QBlock2))
					require.Zero(t, calls)
					require.Equal(t, before, h.snapshot())
					if live {
						h.ingest(request(1, false))
						require.Equal(t, 1, calls)
					}
				})
			}
		}
	}
}

func TestQBlockNONApplicationErrorCloseHeldWrite(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.NotFound, message.TextPlain, bytes.NewReader([]byte("missing"))))
	})
	h.session.contextWriteStart = make(chan struct{}, 1)
	h.session.releaseContextWrite = make(chan struct{})
	done := make(chan struct{})
	go func() { h.ingest(errorServerRequest(t, h, codes.GET)); close(done) }()
	select {
	case <-h.session.contextWriteStart:
	case <-time.After(time.Second):
		t.Fatal("no error write")
	}
	h.session.closeForTest()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not release error write")
	}
	require.Equal(t, serverSnapshot{}, h.snapshot())
	b := h.cc.qblockClient.ownedBudget
	b.mu.Lock()
	used, floor := b.used, b.floor
	b.mu.Unlock()
	require.Equal(t, floor, used)
}

// Exercise the public Do completion path, including original-token release,
// rather than treating an internal callback alone as a completed request.
func TestQBlockNONApplicationErrorDo(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.PUT} {
		t.Run(method.String(), func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 2)}
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req := cc.AcquireMessage(ctx)
			defer cc.ReleaseMessage(req)
			req.SetCode(method)
			req.SetType(message.NonConfirmable)
			req.SetToken(message.Token{0x79})
			require.NoError(t, req.SetPath("/missing"))
			if method != codes.GET {
				req.SetBody(bytes.NewReader([]byte("body")))
			}
			type outcome struct {
				resp *pool.Message
				err  error
			}
			done := make(chan outcome, 1)
			go func() { resp, err := cc.Do(req); done <- outcome{resp, err} }()
			t.Cleanup(func() { cancel() })
			select {
			case <-session.writeCh:
			case <-ctx.Done():
				t.Fatal("request not written")
			}
			sent := session.writesSnapshot()[0]
			calls := 0
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				calls++
				_ = w.SetResponse(codes.NotFound, message.TextPlain, bytes.NewReader([]byte("missing")))
			})
			in := h.cc.AcquireMessage(ctx)
			in.SetCode(sent.code)
			in.SetType(sent.typ)
			in.SetToken(sent.token)
			in.SetMessageID(sent.mid)
			in.ResetOptionsTo(sent.options)
			if len(sent.payload) > 0 {
				in.SetBody(bytes.NewReader(sent.payload))
			}
			h.ingest(in)
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			sentResponse := writes[0]
			response := cc.AcquireMessage(ctx)
			response.SetCode(sentResponse.code)
			response.SetType(sentResponse.typ)
			response.SetMessageID(sentResponse.mid)
			response.SetToken(sentResponse.token)
			response.ResetOptionsTo(sentResponse.options)
			response.SetBody(bytes.NewReader(sentResponse.payload))
			cc.ProcessReceivedMessageWithHandler(response, cc.handleReq)
			select {
			case out := <-done:
				require.NoError(t, out.err)
				require.NotNil(t, out.resp)
				defer cc.ReleaseMessage(out.resp)
				require.Equal(t, codes.NotFound, out.resp.Code())
				body, err := out.resp.ReadBody()
				require.NoError(t, err)
				require.Equal(t, "missing", string(body))
			case <-ctx.Done():
				t.Fatal("Do did not complete")
			}
			require.Equal(t, 1, calls)
			requireQBlockClientEmpty(t, cc)
			require.Empty(t, cc.qblockClient.pendingGETByMID)
			require.Empty(t, cc.qblockClient.workQueue.slots)
			reservations := 0
			cc.tokenReservations.Range(func(_ uint64, _ tokenReservation) bool { reservations++; return true })
			require.Zero(t, reservations)
			require.Len(t, session.writesSnapshot(), 1, "terminal error must not replay payload")
		})
	}
}

func TestQBlockNONMissingMetadataNoResponse(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	req := h.q1(t, 1, 0, false, 4, "body")
	req.Remove(message.Size1)
	req.SetOptionUint32(message.NoResponse, 8)
	h.ingest(req)
	require.Empty(t, h.session.writesSnapshot())
	require.Equal(t, serverSnapshot{}, h.snapshot())
}

func TestQBlockNONApplicationErrorExpiredHeldWrite(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		_ = w.SetResponse(codes.NotFound, message.TextPlain, bytes.NewReader([]byte("missing")))
	})
	h.session.contextWriteStart = make(chan struct{}, 1)
	h.session.releaseContextWrite = make(chan struct{})
	req := errorServerRequest(t, h, codes.GET)
	done := make(chan struct{})
	go func() { h.ingest(req); close(done) }()
	select {
	case <-h.session.contextWriteStart:
	case <-time.After(time.Second):
		h.session.closeForTest()
		t.Fatal("no error write")
	}
	h.advance(h.cc.qblockClient.managerConfig.Transfer.Lifetime)
	select {
	case <-done:
	case <-time.After(time.Second):
		h.session.closeForTest()
		t.Fatal("expiry did not cancel write")
	}
	require.Equal(t, serverSnapshot{}, h.snapshot())
	b := h.cc.qblockClient.ownedBudget
	b.mu.Lock()
	used, floor := b.used, b.floor
	b.mu.Unlock()
	require.Equal(t, floor, used)
}

func TestQBlockNONRejectionMIDHeldWrite(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.cc.msgID.Store(76)
	h.session.contextWriteStart = make(chan struct{}, 1)
	h.session.releaseContextWrite = make(chan struct{})
	req := h.q1(t, 1, 0, false, 4, "body")
	req.Remove(message.Size1)
	done := make(chan struct{})
	go func() { h.ingest(req); close(done) }()
	t.Cleanup(func() { h.session.closeForTest(); <-done })
	select {
	case <-h.session.contextWriteStart:
	case <-time.After(time.Second):
		t.Fatal("no rejection write")
	}
	c := h.cc.qblockClient
	c.mu.Lock()
	err := c.reserveMIDLocked(77)
	c.mu.Unlock()
	require.Error(t, err, "held rejection must reserve its MID")
	close(h.session.releaseContextWrite)
	<-done
	c.mu.Lock()
	err = c.reserveMIDLocked(77)
	c.mu.Unlock()
	require.NoError(t, err, "completed rejection must release its MID")
}
