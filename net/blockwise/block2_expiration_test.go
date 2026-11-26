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

func TestBlock2FinalWithoutTransfer(t *testing.T) {
	tests := []struct {
		name        string
		requestSZX  SZX
		requestNum  int64
		hasRequest  bool
		responseSZX SZX
		responseNum int64
		forward     bool
	}{
		{name: "initial block", responseSZX: SZX16, forward: true},
		{name: "unsolicited final block", responseSZX: SZX16, responseNum: 2},
		{name: "explicit final block", hasRequest: true, requestSZX: SZX16, requestNum: 2, responseSZX: SZX16, responseNum: 2, forward: true},
		{name: "different block", hasRequest: true, requestSZX: SZX16, requestNum: 1, responseSZX: SZX16, responseNum: 2},
		{name: "same offset smaller response size", hasRequest: true, requestSZX: SZX32, requestNum: 1, responseSZX: SZX16, responseNum: 2, forward: true},
		{name: "same number different offset", hasRequest: true, requestSZX: SZX32, requestNum: 2, responseSZX: SZX16, responseNum: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := newTestClient()
			token := message.Token{1}
			bw := New(cc, time.Minute, func(err error) { require.NoError(t, err) }, func(message.Token) (*pool.Message, bool) {
				req := cc.AcquireMessage(t.Context())
				req.SetCode(codes.GET)
				req.SetToken(token)
				if tt.hasRequest {
					value, err := EncodeBlockOption(tt.requestSZX, tt.requestNum, false)
					require.NoError(t, err)
					req.SetOptionUint32(message.Block2, value)
				}
				return req, true
			})
			resp := cc.AcquireMessage(t.Context())
			defer cc.ReleaseMessage(resp)
			resp.SetCode(codes.Content)
			resp.SetToken(token)
			value, err := EncodeBlockOption(tt.responseSZX, tt.responseNum, false)
			require.NoError(t, err)
			resp.SetOptionUint32(message.Block2, value)
			resp.SetBody(bytes.NewReader([]byte("last")))
			w := responsewriter.New(cc.AcquireMessage(t.Context()), cc)
			defer func() { cc.ReleaseMessage(w.Message()) }()
			called := false
			bw.Handle(w, resp, SZX1024, 1024, nil, func(_ *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
				called = true
				body, err := r.ReadBody()
				require.NoError(t, err)
				require.Equal(t, []byte("last"), body)
			})
			require.Equal(t, tt.forward, called)
			if !tt.forward {
				require.Equal(t, codes.GET, w.Message().Code())
				block, err := w.Message().GetOptionUint32(message.Block2)
				require.NoError(t, err)
				_, num, _, err := DecodeBlockOption(block)
				require.NoError(t, err)
				require.Zero(t, num)
			}
		})
	}
}

func TestBlock2ExpiredObserveResponseRestarts(t *testing.T) {
	cc := newTestClient()
	token := message.Token{1}
	bw := New(cc, time.Minute, func(err error) { require.NoError(t, err) }, func(message.Token) (*pool.Message, bool) {
		req := cc.AcquireMessage(t.Context())
		req.SetCode(codes.GET)
		req.SetToken(token)
		req.SetObserve(0)
		return req, true
	})
	var delivered [][]byte
	var contentFormats []message.MediaType
	send := func(num int64, more bool, body []byte, contentFormat message.MediaType) *pool.Message {
		resp := cc.AcquireMessage(t.Context())
		defer cc.ReleaseMessage(resp)
		resp.SetCode(codes.Content)
		resp.SetToken(token)
		value, err := EncodeBlockOption(SZX16, num, more)
		require.NoError(t, err)
		resp.SetOptionUint32(message.Block2, value)
		resp.SetContentFormat(contentFormat)
		resp.SetBody(bytes.NewReader(body))
		w := responsewriter.New(cc.AcquireMessage(t.Context()), cc)
		bw.Handle(w, resp, SZX16, 1024, nil, func(_ *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
			got, err := r.ReadBody()
			require.NoError(t, err)
			delivered = append(delivered, got)
			format, err := r.ContentFormat()
			require.NoError(t, err)
			contentFormats = append(contentFormats, format)
		})
		return w.Message()
	}
	first := bytes.Repeat([]byte{1}, 16)
	last := []byte{2, 3}
	response := send(0, true, first, message.TextPlain)
	cc.ReleaseMessage(response)
	require.Empty(t, delivered)
	bw.CheckExpirations(time.Now().Add(2 * time.Minute))

	response = send(1, false, last, message.AppJSON)
	require.Empty(t, delivered)
	require.Equal(t, codes.GET, response.Code())
	block, err := response.GetOptionUint32(message.Block2)
	require.NoError(t, err)
	_, num, _, err := DecodeBlockOption(block)
	require.NoError(t, err)
	require.Zero(t, num)
	require.False(t, response.HasOption(message.Observe))
	cc.ReleaseMessage(response)

	response = send(0, true, first, message.TextPlain)
	cc.ReleaseMessage(response)
	response = send(1, false, last, message.TextPlain)
	cc.ReleaseMessage(response)
	require.Equal(t, [][]byte{append(first, last...)}, delivered)
	require.Equal(t, []message.MediaType{message.TextPlain}, contentFormats)
}
