package blockwise

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/stretchr/testify/require"
)

func TestBlock2BodyMethodContinuationRequiresStoredResponse(t *testing.T) {
	const (
		missing = iota
		expired
		ambiguous
		matched
		changedToken
		nilRemote
	)
	methods := []codes.Code{codes.FETCH, codes.POST, codes.PUT, codes.PATCH, codes.IPATCH}
	for _, method := range methods {
		t.Run(method.String(), func(t *testing.T) {
			for _, scenario := range []struct {
				name string
				kind int
			}{
				{"missing", missing},
				{"expired", expired},
				{"ambiguous", ambiguous},
				{"matched", matched},
				{"new token", changedToken},
				{"nil remote", nilRemote},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					r := newBlock1Receiver(t, time.Minute)
					remote := r.remote
					if scenario.kind == nilRemote {
						remote = nil
					}
					body := bytes.Repeat([]byte{0x42}, 2*testBlock1Size+100)
					calls := 0
					handler := func(w *responsewriter.ResponseWriter[*testClient], req *pool.Message) {
						calls++
						requestBody, err := req.ReadBody()
						require.NoError(t, err)
						require.Equal(t, []byte("query"), requestBody)
						require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(body)))
					}
					send := func(token message.Token, continuation bool) (codes.Code, []byte) {
						req := r.cc.AcquireMessage(t.Context())
						defer r.cc.ReleaseMessage(req)
						req.SetCode(method)
						req.SetToken(token)
						require.NoError(t, req.SetPath("/resource"))
						if continuation {
							block, err := EncodeBlockOption(SZX1024, 1, false)
							require.NoError(t, err)
							req.SetOptionUint32(message.Block2, block)
						} else {
							req.SetBody(bytes.NewReader([]byte("query")))
						}
						w := responsewriter.New(r.cc.AcquireMessage(t.Context()), r.cc)
						defer func() { r.cc.ReleaseMessage(w.Message()) }()
						w.Message().SetToken(token)
						r.bw.Handle(w, req, SZX1024, 64*1024, remote, handler)
						var payload []byte
						if w.Message().Body() != nil {
							var err error
							payload, err = w.Message().ReadBody()
							require.NoError(t, err)
						}
						return w.Message().Code(), payload
					}

					initialToken := message.Token{1}
					if scenario.kind == expired || scenario.kind == ambiguous || scenario.kind == matched || scenario.kind == changedToken {
						code, payload := send(initialToken, false)
						require.Equal(t, codes.Content, code)
						require.Equal(t, body[:testBlock1Size], payload)
					}
					if scenario.kind == expired {
						r.bw.sendingMessagesCache.Range(func(_ uint64, el *cache.Element[*sendingMessage]) bool {
							el.ValidUntil.Store(time.Now().Add(-time.Second))
							return true
						})
					}
					if scenario.kind == ambiguous {
						code, _ := send(message.Token{2}, false)
						require.Equal(t, codes.Content, code)
					}
					token := message.Token{3}
					if scenario.kind == matched || scenario.kind == expired {
						token = initialToken
					}
					code, payload := send(token, true)
					if scenario.kind == matched || scenario.kind == changedToken {
						require.Equal(t, codes.Content, code)
						require.Equal(t, body[testBlock1Size:2*testBlock1Size], payload)
						require.Equal(t, 1, calls)
						return
					}
					require.Equal(t, codes.RequestEntityIncomplete, code)
					switch scenario.kind {
					case ambiguous:
						require.Equal(t, 2, calls)
					case expired:
						require.Equal(t, 1, calls)
					default:
						require.Zero(t, calls)
					}
				})
			}
		})
	}
}
