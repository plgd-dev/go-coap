package blockwise

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestBlock2ContinuationCannotEndAnotherTransfer(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.FETCH} {
		t.Run(method.String(), func(t *testing.T) {
			for _, test := range []struct {
				name         string
				initialToken message.Token
				otherToken   message.Token
				block        int64
				invalid      bool
			}{
				{"invalid changed token", message.Token{1}, message.Token{2}, 10, true},
				{"invalid same token", message.Token{1}, message.Token{1}, 10, true},
				{"invalid tokenless", nil, nil, 10, true},
				{"final changed token", message.Token{1}, message.Token{2}, 2, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					r := newBlock1Receiver(t, time.Minute)
					payload := bytes.Repeat([]byte{1}, 2*testBlock1Size+100)
					calls := 0
					var reported []error
					r.bw.errors = func(err error) { reported = append(reported, err) }
					handler := func(w *responsewriter.ResponseWriter[*testClient], _ *pool.Message) {
						calls++
						require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(payload)))
					}
					send := func(token message.Token, block *uint32, body []byte) block1Block2Exchange {
						if method == codes.GET {
							return r.get(token, block, handler)
						}
						req := r.cc.AcquireMessage(t.Context())
						defer r.cc.ReleaseMessage(req)
						req.SetCode(method)
						req.SetToken(token)
						require.NoError(t, req.SetPath("/big"))
						if block != nil {
							req.SetOptionUint32(message.Block2, *block)
						}
						if body != nil {
							req.SetBody(bytes.NewReader(body))
						}
						w := responsewriter.New(r.cc.AcquireMessage(t.Context()), r.cc)
						w.Message().SetToken(token)
						defer func() { r.cc.ReleaseMessage(w.Message()) }()
						r.bw.Handle(w, req, SZX1024, 64*1024, r.remote, handler)
						response := block1Block2Exchange{code: w.Message().Code()}
						if w.Message().Body() != nil {
							var err error
							response.payload, err = w.Message().ReadBody()
							require.NoError(t, err)
						}
						return response
					}
					encode := func(num int64) *uint32 {
						block, err := EncodeBlockOption(SZX1024, num, false)
						require.NoError(t, err)
						return &block
					}
					first := send(test.initialToken, nil, []byte("query"))
					require.Equal(t, codes.Content, first.code)
					require.Equal(t, payload[:testBlock1Size], first.payload)
					other := send(test.otherToken, encode(test.block), nil)
					if test.invalid {
						require.Len(t, reported, 1)
					} else {
						require.Empty(t, reported)
						require.Equal(t, payload[2*testBlock1Size:], other.payload)
					}
					require.Equal(t, 1, r.bw.sendingMessagesCache.Length())
					next := send(test.initialToken, encode(1), nil)
					require.Equal(t, codes.Content, next.code)
					require.Equal(t, payload[testBlock1Size:2*testBlock1Size], next.payload)
					last := send(test.initialToken, encode(2), nil)
					require.Equal(t, codes.Content, last.code)
					require.Equal(t, payload[2*testBlock1Size:], last.payload)
					require.Equal(t, 1, calls)
					require.Zero(t, r.bw.sendingMessagesCache.Length())
				})
			}
		})
	}
}

func TestBlock2CompletedTransferAllowsNewRequestWithOriginalToken(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.PUT, codes.FETCH, codes.PATCH, codes.IPATCH} {
		for _, useBlock2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/block2=%t", method, useBlock2), func(t *testing.T) {
				r := newBlock1Receiver(t, time.Minute)
				var reported []error
				r.bw.errors = func(err error) { reported = append(reported, err) }
				calls := 0
				payload := func(version byte) []byte {
					return bytes.Repeat([]byte{version}, 2*testBlock1Size+100)
				}
				handler := func(w *responsewriter.ResponseWriter[*testClient], _ *pool.Message) {
					calls++
					require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(payload(byte(calls)))))
				}
				send := func(token message.Token, block *uint32) block1Block2Exchange {
					req := r.cc.AcquireMessage(t.Context())
					defer r.cc.ReleaseMessage(req)
					req.SetCode(method)
					req.SetToken(token)
					require.NoError(t, req.SetPath("/big"))
					if block != nil {
						req.SetOptionUint32(message.Block2, *block)
					}
					if method != codes.GET && !isBlock2Continuation(req) {
						req.SetBody(bytes.NewReader([]byte("query")))
					}
					respMsg := r.cc.AcquireMessage(t.Context())
					respMsg.SetToken(token)
					w := responsewriter.New(respMsg, r.cc)
					defer func() { r.cc.ReleaseMessage(w.Message()) }()
					r.bw.Handle(w, req, SZX1024, 64*1024, r.remote, handler)
					body, err := w.Message().ReadBody()
					require.NoError(t, err)
					return block1Block2Exchange{code: w.Message().Code(), token: w.Message().Token(), payload: body}
				}
				block := func(num int64) *uint32 {
					v, err := EncodeBlockOption(SZX1024, num, false)
					require.NoError(t, err)
					return &v
				}
				original := message.Token{1}
				require.Equal(t, payload(1)[:testBlock1Size], send(original, nil).payload)
				require.Equal(t, payload(1)[2*testBlock1Size:], send(message.Token{2}, block(2)).payload)
				require.Equal(t, payload(1)[testBlock1Size:2*testBlock1Size], send(original, block(1)).payload)
				require.Equal(t, 1, calls)

				var initialBlock *uint32
				if useBlock2 {
					initialBlock = block(0)
				}
				fresh := send(original, initialBlock)
				require.Equal(t, codes.Content, fresh.code)
				require.Equal(t, payload(2)[:testBlock1Size], fresh.payload)
				require.Equal(t, 2, calls)
				next := send(original, block(1))
				require.Equal(t, payload(2)[testBlock1Size:2*testBlock1Size], next.payload)
				require.Empty(t, reported)
			})
		}
	}
}

func TestBlock2CompletedTransferAllowsNewBlock1Request(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	var reported []error
	r.bw.errors = func(err error) { reported = append(reported, err) }
	calls := 0
	handler := func(w *responsewriter.ResponseWriter[*testClient], _ *pool.Message) {
		calls++
		body := bytes.Repeat([]byte{byte(calls)}, 2*testBlock1Size+100)
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(body)))
	}
	block := func(num int64, more bool) *uint32 {
		v, err := EncodeBlockOption(SZX1024, num, more)
		require.NoError(t, err)
		return &v
	}
	token := message.Token{1}
	first := r.post(token, nil, nil, []byte("first"), handler)
	require.Equal(t, codes.Content, first.code)
	complete := r.post(message.Token{2}, nil, block(2, false), nil, handler)
	require.Equal(t, codes.Content, complete.code)
	require.Equal(t, 1, calls)

	part := r.post(token, block(0, true), nil, bytes.Repeat([]byte{1}, testBlock1Size), handler)
	require.Equal(t, codes.Continue, part.code)
	last := r.post(token, block(1, false), nil, []byte("last"), handler)
	require.Equal(t, codes.Content, last.code)
	require.Equal(t, bytes.Repeat([]byte{2}, testBlock1Size), last.payload)
	require.Equal(t, 2, calls)
	require.Empty(t, reported)
}

func TestBlockWisePostBlock1Block2RequestTagSeparatesTransfers(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	payloads := [][]byte{
		bytes.Repeat([]byte{1}, 2*testBlock1Size+100),
		bytes.Repeat([]byte{2}, 2*testBlock1Size+100),
	}
	handlerCalls := 0
	handler := func(w *responsewriter.ResponseWriter[*testClient], _ *pool.Message) {
		require.Less(t, handlerCalls, len(payloads))
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(payloads[handlerCalls])))
		handlerCalls++
	}
	send := func(token, requestTag message.Token, block1, block2 *uint32, body []byte) block1Block2Exchange {
		req := r.cc.AcquireMessage(t.Context())
		defer r.cc.ReleaseMessage(req)
		req.SetCode(codes.POST)
		req.SetToken(token)
		req.SetType(message.Confirmable)
		require.NoError(t, req.SetPath("/big"))
		require.NoError(t, req.SetRequestTag(requestTag))
		if block1 != nil {
			req.SetOptionUint32(message.Block1, *block1)
		}
		if block2 != nil {
			req.SetOptionUint32(message.Block2, *block2)
		}
		if body != nil {
			req.SetBody(bytes.NewReader(body))
		}
		respMsg := r.cc.AcquireMessage(t.Context())
		respMsg.SetToken(token)
		w := responsewriter.New(respMsg, r.cc)
		defer func() { r.cc.ReleaseMessage(w.Message()) }()
		r.bw.Handle(w, req, SZX1024, 64*1024, r.remote, handler)

		response := block1Block2Exchange{code: w.Message().Code(), token: w.Message().Token()}
		if w.Message().Body() != nil {
			var err error
			response.payload, err = w.Message().ReadBody()
			require.NoError(t, err)
		}
		return response
	}
	block1First, err := EncodeBlockOption(SZX1024, 0, true)
	require.NoError(t, err)
	block1Last, err := EncodeBlockOption(SZX1024, 1, false)
	require.NoError(t, err)
	block2Next, err := EncodeBlockOption(SZX1024, 1, false)
	require.NoError(t, err)
	firstTag := message.Token("first")
	secondTag := message.Token("second")
	requestBody := bytes.Repeat([]byte("q"), testBlock1Size+100)

	require.Equal(t, codes.Continue, send(message.Token{1}, firstTag, &block1First, nil, requestBody[:testBlock1Size]).code)
	require.Equal(t, codes.Continue, send(message.Token{2}, secondTag, &block1First, nil, requestBody[:testBlock1Size]).code)
	first := send(message.Token{1}, firstTag, &block1Last, nil, requestBody[testBlock1Size:])
	second := send(message.Token{2}, secondTag, &block1Last, nil, requestBody[testBlock1Size:])
	require.Equal(t, codes.Content, first.code)
	require.Equal(t, codes.Content, second.code)
	require.Equal(t, payloads[0][:testBlock1Size], first.payload)
	require.Equal(t, payloads[1][:testBlock1Size], second.payload)

	firstContinuation := send(message.Token{3}, firstTag, nil, &block2Next, nil)
	secondContinuation := send(message.Token{4}, secondTag, nil, &block2Next, nil)
	require.Equal(t, payloads[0][testBlock1Size:2*testBlock1Size], firstContinuation.payload)
	require.Equal(t, payloads[1][testBlock1Size:2*testBlock1Size], secondContinuation.payload)
	require.Equal(t, 2, handlerCalls, "Block2 continuations must use the response for their Request-Tag")
}
