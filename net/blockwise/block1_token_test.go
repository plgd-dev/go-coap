package blockwise

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestBlock1HandlerReceivesFinalExchangeToken(t *testing.T) {
	tests := []struct {
		name        string
		first, last message.Token
	}{
		{name: "changed token", first: message.Token{1}, last: message.Token{2}},
		{name: "same token", first: message.Token{1}, last: message.Token{1}},
		{name: "tokenless", first: nil, last: nil},
	}

	for _, code := range []codes.Code{codes.POST, codes.PUT, codes.FETCH, codes.PATCH, codes.IPATCH} {
		for _, test := range tests {
			t.Run(code.String()+"/"+test.name, func(t *testing.T) {
				r := newBlock1Receiver(t, time.Minute)
				var calls int
				send := func(num int64, more bool, token message.Token) {
					req := r.cc.AcquireMessage(t.Context())
					defer r.cc.ReleaseMessage(req)
					req.SetCode(code)
					req.SetToken(token)
					req.SetType(message.Confirmable)
					require.NoError(t, req.SetPath("/resource"))
					value, err := EncodeBlockOption(SZX1024, num, more)
					require.NoError(t, err)
					req.SetOptionUint32(message.Block1, value)
					req.SetBody(bytes.NewReader(bytes.Repeat([]byte{byte(num)}, testBlock1Size)))
					resp := r.cc.AcquireMessage(t.Context())
					resp.SetToken(token)
					w := responsewriter.New(resp, r.cc)
					defer func() { r.cc.ReleaseMessage(w.Message()) }()
					r.bw.Handle(w, req, SZX1024, 64*1024, r.remote, func(w *responsewriter.ResponseWriter[*testClient], request *pool.Message) {
						calls++
						require.Equal(t, test.last, request.Token())
						body, err := request.ReadBody()
						require.NoError(t, err)
						require.Len(t, body, 2*testBlock1Size)
						require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
					})
					if more {
						require.Equal(t, codes.Continue, w.Message().Code())
					} else {
						require.Equal(t, codes.Changed, w.Message().Code())
					}
				}

				send(0, true, test.first)
				send(1, false, test.last)
				require.Equal(t, 1, calls)
			})
		}
	}
}
