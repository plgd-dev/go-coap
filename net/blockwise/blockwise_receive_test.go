package blockwise

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBlock1Size = 1024

type block1Receiver struct {
	t         *testing.T
	cc        *testClient
	bw        *BlockWise[*testClient]
	token     message.Token
	remote    net.Addr
	delivered [][]byte
}

func newBlock1Receiver(t *testing.T, expiration time.Duration) *block1Receiver {
	cc := newTestClient()
	return &block1Receiver{
		t:      t,
		cc:     cc,
		bw:     New(cc, expiration, func(error) {}, nil),
		token:  message.Token{0x01},
		remote: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683},
	}
}

func (b *block1Receiver) exchangeKey() uint64 {
	return b.exchangeKeyWithPath("")
}

func (b *block1Receiver) exchangeKeyWithPath(path string) uint64 {
	req := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(req)
	req.SetCode(codes.PUT)
	req.SetToken(b.token)
	if path != "" {
		require.NoError(b.t, req.SetPath(path))
	}
	req.SetOptionUint32(message.Block1, 0)
	key, err := getMessageExchangeKey(req, b.remote)
	require.NoError(b.t, err)
	return key
}

type block1Response struct {
	code   codes.Code
	size1  uint32
	block1 *uint32
}

func (b *block1Receiver) send(num int64, more bool, size1 ...uint32) block1Response {
	return b.sendPayload(num, more, bytes.Repeat([]byte{byte(num)}, testBlock1Size), size1...)
}

func (b *block1Receiver) sendPayload(num int64, more bool, payload []byte, size1 ...uint32) block1Response {
	return b.sendPayloadWithPath(num, more, payload, "", size1...)
}

func (b *block1Receiver) sendPayloadWithPath(num int64, more bool, payload []byte, path string, size1 ...uint32) block1Response {
	req := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(req)
	req.SetCode(codes.PUT)
	req.SetToken(b.token)
	req.SetType(message.Confirmable)
	if path != "" {
		require.NoError(b.t, req.SetPath(path))
	}
	block, err := EncodeBlockOption(SZX1024, num, more)
	require.NoError(b.t, err)
	req.SetOptionUint32(message.Block1, block)
	for _, v := range size1 {
		req.SetOptionUint32(message.Size1, v)
	}
	req.SetBody(bytes.NewReader(payload))

	w := responsewriter.New(b.cc.AcquireMessage(b.t.Context()), b.cc)
	defer func() { b.cc.ReleaseMessage(w.Message()) }()
	b.bw.Handle(w, req, SZX1024, 64*1024, b.remote, func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
		body, errR := r.ReadBody()
		require.NoError(b.t, errR)
		b.delivered = append(b.delivered, body)
		require.NoError(b.t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	})
	respSize1, _ := w.Message().GetOptionUint32(message.Size1)
	resp := block1Response{code: w.Message().Code(), size1: respSize1}
	if v, err := w.Message().GetOptionUint32(message.Block1); err == nil {
		resp.block1 = &v
	}
	return resp
}

// sendBlocks sends blocks 0..n-1, all with the M bit set.
func (b *block1Receiver) sendBlocks(n int64) {
	for num := range n {
		require.Equal(b.t, codes.Continue, b.send(num, true).code, "block %v", num)
	}
}

func TestBlockWiseReceiveExpiredTransfer(t *testing.T) {
	tests := []struct {
		name string
		more bool
	}{
		{name: "intermediate block", more: true},
		{name: "last block", more: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			r.sendBlocks(3)
			r.bw.CheckExpirations(time.Now().Add(2 * time.Minute))

			// The transfer state is gone, so the block must not be
			// acknowledged with 2.31 nor forwarded as a complete body.
			require.Equal(t, codes.RequestEntityIncomplete, r.send(3, tt.more).code)
			require.Empty(t, r.delivered)

			// The transfer can be restarted from block 0.
			r.sendBlocks(2)
			require.Equal(t, codes.Changed, r.send(2, false).code)
			require.Len(t, r.delivered, 1)
			require.Len(t, r.delivered[0], 3*testBlock1Size)
		})
	}
}

func TestBlockWiseReceiveMissingBlock(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.sendBlocks(2)
	require.Equal(t, codes.RequestEntityIncomplete, r.send(3, true).code)
	require.Equal(t, codes.RequestEntityIncomplete, r.send(2, true).code, "state must be discarded after a gap")
	require.Empty(t, r.delivered)
}

func TestBlockWiseReceiveLastBlockBehindPayload(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.sendBlocks(3)
	require.Equal(t, codes.RequestEntityIncomplete, r.send(1, false).code)
	require.Empty(t, r.delivered)
}

func TestBlockWiseReceiveDuplicateBlock(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.sendBlocks(2)
	require.Equal(t, codes.Continue, r.send(1, true).code)
	require.Equal(t, codes.Changed, r.send(2, false).code)
	require.Len(t, r.delivered, 1)
	require.Len(t, r.delivered[0], 3*testBlock1Size)
	require.Equal(t, byte(1), r.delivered[0][testBlock1Size])
	require.Equal(t, byte(2), r.delivered[0][2*testBlock1Size])
}

func TestBlockWiseReceiveRollingExpiration(t *testing.T) {
	tests := []struct {
		name     string
		rolling  bool
		wantLast codes.Code
	}{
		{name: "fixed", rolling: false, wantLast: codes.RequestEntityIncomplete},
		{name: "rolling", rolling: true, wantLast: codes.Changed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newBlock1Receiver(t, time.Hour)
			r.bw.SetRollingExpiration(tt.rolling)
			r.sendBlocks(1)
			e := r.bw.receivingMessagesCache.Load(r.exchangeKey())
			require.NotNil(t, e)
			pinned := time.Now().Add(time.Minute)
			e.ValidUntil.Store(pinned)
			require.Equal(t, codes.Continue, r.send(1, true).code)
			r.bw.CheckExpirations(pinned.Add(time.Nanosecond))
			require.Equal(t, tt.wantLast, r.send(2, false).code)
			if tt.rolling {
				require.Len(t, r.delivered, 1)
				require.Len(t, r.delivered[0], 3*testBlock1Size)
			} else {
				require.Empty(t, r.delivered)
			}
		})
	}
}

func TestBlockWiseReceiveRollingExpirationIdle(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.bw.SetRollingExpiration(true)
	r.sendBlocks(2)
	r.bw.CheckExpirations(time.Now().Add(2 * time.Minute))
	require.Equal(t, codes.RequestEntityIncomplete, r.send(2, true).code)
}

func TestBlockWiseReceiveRollingExpirationOnlyOnNewData(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.bw.SetRollingExpiration(true)
	r.sendBlocks(2)
	e := r.bw.receivingMessagesCache.Load(r.exchangeKey())
	require.NotNil(t, e)
	pinned := time.Now().Add(time.Second)
	e.ValidUntil.Store(pinned)

	require.Equal(t, codes.Continue, r.send(1, true).code)
	require.True(t, e.ValidUntil.Load().Equal(pinned), "duplicate block must not extend the expiration")

	require.Equal(t, codes.Continue, r.send(2, true).code)
	require.True(t, e.ValidUntil.Load().After(pinned), "new block must extend the expiration")
}

func TestBlockWiseReceiveInvalidNonFinalBlock(t *testing.T) {
	payloads := map[string][]byte{
		"empty": nil,
		"short": make([]byte, testBlock1Size/2),
		"long":  make([]byte, testBlock1Size+1),
	}
	for name, payload := range payloads {
		t.Run(name+" first block", func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			require.Equal(t, codes.RequestEntityIncomplete, r.sendPayload(0, true, payload).code)
			require.Nil(t, r.bw.receivingMessagesCache.Load(r.exchangeKey()))
		})
		t.Run(name+" next block", func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			r.bw.SetRollingExpiration(true)
			r.sendBlocks(2)
			require.Equal(t, codes.RequestEntityIncomplete, r.sendPayload(2, true, payload).code)
			require.Nil(t, r.bw.receivingMessagesCache.Load(r.exchangeKey()), "state must be discarded")
			require.Empty(t, r.delivered)
		})
	}
}

func TestBlockWiseReceiveMaxBodySize(t *testing.T) {
	const limit = 2 * testBlock1Size
	t.Run("payload exceeds limit", func(t *testing.T) {
		r := newBlock1Receiver(t, time.Minute)
		r.bw.SetMaxReceiveBodySize(limit)
		r.sendBlocks(2)
		resp := r.send(2, true)
		require.Equal(t, codes.RequestEntityTooLarge, resp.code)
		require.Equal(t, uint32(limit), resp.size1)
		require.Equal(t, codes.RequestEntityIncomplete, r.send(3, false).code, "state must be discarded")
		require.Empty(t, r.delivered)
	})
	t.Run("size1 exceeds limit", func(t *testing.T) {
		r := newBlock1Receiver(t, time.Minute)
		r.bw.SetMaxReceiveBodySize(limit)
		resp := r.send(0, true, limit+1)
		require.Equal(t, codes.RequestEntityTooLarge, resp.code)
		require.Equal(t, uint32(limit), resp.size1)
	})
	t.Run("within limit", func(t *testing.T) {
		r := newBlock1Receiver(t, time.Minute)
		r.bw.SetMaxReceiveBodySize(limit)
		require.Equal(t, codes.Continue, r.send(0, true, limit).code)
		require.Equal(t, codes.Changed, r.send(1, false).code)
		require.Len(t, r.delivered, 1)
	})
}

func TestBlockWiseReceiveMaxBodySizeSingleBlock(t *testing.T) {
	const limit = testBlock1Size / 2
	tests := []struct {
		name     string
		size     int
		size1    []uint32
		wantCode codes.Code
	}{
		{name: "payload exceeds limit", size: limit + 1, wantCode: codes.RequestEntityTooLarge},
		{name: "size1 exceeds limit", size: limit, size1: []uint32{limit + 1}, wantCode: codes.RequestEntityTooLarge},
		{name: "within limit", size: limit, size1: []uint32{limit}, wantCode: codes.Changed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			r.bw.SetMaxReceiveBodySize(limit)
			resp := r.sendPayload(0, false, make([]byte, tt.size), tt.size1...)
			require.Equal(t, tt.wantCode, resp.code)
			if tt.wantCode == codes.RequestEntityTooLarge {
				require.Equal(t, uint32(limit), resp.size1)
				require.Empty(t, r.delivered)
				return
			}
			require.Len(t, r.delivered, 1)
			require.Len(t, r.delivered[0], tt.size)
		})
	}
}

func TestBlockWiseReceiveTokenless(t *testing.T) {
	const limit = testBlock1Size / 2
	r := newBlock1Receiver(t, time.Minute)
	r.token = nil
	r.bw.SetMaxReceiveBodySize(testBlock1Size * 2)

	require.Equal(t, codes.Continue, r.send(0, true).code)
	require.Empty(t, r.delivered)
	require.NotNil(t, r.bw.receivingMessagesCache.Load(r.exchangeKey()))
	require.Equal(t, codes.Changed, r.sendPayload(1, false, make([]byte, limit)).code)
	require.Len(t, r.delivered, 1)
	require.Len(t, r.delivered[0], testBlock1Size+limit)

	exchangeKey := r.exchangeKey()
	require.Nil(t, r.bw.receivingMessagesCache.Load(exchangeKey))
	require.Nil(t, r.bw.sendingMessagesCache.Load(exchangeKey))

	t.Run("rejects oversized payload", func(t *testing.T) {
		r := newBlock1Receiver(t, time.Minute)
		r.token = nil
		r.bw.SetMaxReceiveBodySize(limit)
		resp := r.sendPayload(0, false, make([]byte, testBlock1Size))
		require.Equal(t, codes.RequestEntityTooLarge, resp.code)
		require.Equal(t, uint32(limit), resp.size1)
		require.Empty(t, r.delivered)
	})

	t.Run("requires earlier blocks", func(t *testing.T) {
		r := newBlock1Receiver(t, time.Minute)
		r.token = nil
		resp := r.sendPayload(1, true, make([]byte, testBlock1Size))
		require.Equal(t, codes.RequestEntityIncomplete, resp.code)
		require.Empty(t, r.delivered)
	})
}

func TestBlockWiseReceiveBlock1ContinuationWithoutPath(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.token = message.Token{0x01}
	require.Equal(t, codes.Continue, r.send(0, true).code)

	r.token = message.Token{0x02}
	resp := r.send(1, false)
	require.Equal(t, codes.Changed, resp.code)
	wantBlock1, err := EncodeBlockOption(SZX1024, 1, false)
	require.NoError(t, err)
	require.NotNil(t, resp.block1)
	require.Equal(t, wantBlock1, *resp.block1)
	require.Len(t, r.delivered, 1)
	require.Len(t, r.delivered[0], 2*testBlock1Size)
}

func TestBlockWiseReceiveBlock1DifferentPath(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	require.Equal(t, codes.Continue, r.sendPayloadWithPath(0, true, bytes.Repeat([]byte{0}, testBlock1Size), "/first").code)
	require.Equal(t, codes.RequestEntityIncomplete, r.sendPayloadWithPath(1, false, bytes.Repeat([]byte{1}, testBlock1Size), "/second").code)
	require.Empty(t, r.delivered)
}

type block1Block2Exchange struct {
	code    codes.Code
	token   message.Token
	block1  *uint32
	block2  *uint32
	payload []byte
}

func (b *block1Receiver) post(token message.Token, block1, block2 *uint32, payload []byte, h func(*responsewriter.ResponseWriter[*testClient], *pool.Message)) block1Block2Exchange {
	req := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(req)
	req.SetCode(codes.POST)
	req.SetToken(token)
	req.SetType(message.Confirmable)
	require.NoError(b.t, req.SetPath("/big"))
	if block1 != nil {
		req.SetOptionUint32(message.Block1, *block1)
	}
	if block2 != nil {
		req.SetOptionUint32(message.Block2, *block2)
	}
	if payload != nil {
		req.SetBody(bytes.NewReader(payload))
	}

	// like udp/tcp conn, the response is prepared with the request token
	respMsg := b.cc.AcquireMessage(b.t.Context())
	respMsg.SetToken(token)
	w := responsewriter.New(respMsg, b.cc)
	defer func() { b.cc.ReleaseMessage(w.Message()) }()
	b.bw.Handle(w, req, SZX1024, 64*1024, b.remote, h)

	resp := block1Block2Exchange{code: w.Message().Code(), token: w.Message().Token()}
	// a response always echoes the token of the request it answers (RFC 7252 §5.3.2)
	require.Equal(b.t, token, resp.token)
	if v, err := w.Message().GetOptionUint32(message.Block1); err == nil {
		resp.block1 = &v
	}
	if v, err := w.Message().GetOptionUint32(message.Block2); err == nil {
		resp.block2 = &v
	}
	if w.Message().Body() != nil {
		body, err := w.Message().ReadBody()
		require.NoError(b.t, err)
		resp.payload = body
	}
	return resp
}

// TestBlockWiseReceivePostBlock1ThenBlock2 covers RFC 7959 section 2.7 (combined
// Block1 and Block2): a large POST request is uploaded with Block1, the large
// response is returned with Block2 and the client changes the token for every
// request, as libcoap does.
func TestBlockWiseReceivePostBlock1ThenBlock2(t *testing.T) {
	const requestBlocks = 3
	const responseSize = 2*testBlock1Size + 100
	request := make([]byte, 0, requestBlocks*testBlock1Size)
	for i := range requestBlocks {
		request = append(request, bytes.Repeat([]byte{byte(i)}, testBlock1Size)...)
	}
	response := make([]byte, responseSize)
	for i := range response {
		response[i] = byte(i % 251)
	}

	encode := func(num int64, more bool) *uint32 {
		v, err := EncodeBlockOption(SZX1024, num, more)
		require.NoError(t, err)
		return &v
	}

	tests := []struct {
		name   string
		tokens []message.Token
	}{
		{
			name:   "changing tokens",
			tokens: []message.Token{{0x01}, {0x02}, {0x03}, {0x04}, {0x05}},
		},
		{
			name:   "same token",
			tokens: []message.Token{{0x01}, {0x01}, {0x01}, {0x01}, {0x01}},
		},
		{
			name:   "tokenless",
			tokens: []message.Token{nil, nil, nil, nil, nil},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			handlerCalls := 0
			h := func(w *responsewriter.ResponseWriter[*testClient], req *pool.Message) {
				handlerCalls++
				require.Equal(t, codes.POST, req.Code())
				body, err := req.ReadBody()
				require.NoError(t, err)
				require.Equal(t, request, body)
				require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(response)))
			}

			for num := range int64(requestBlocks) {
				more := num < requestBlocks-1
				resp := r.post(tt.tokens[num], encode(num, more), nil, request[num*testBlock1Size:(num+1)*testBlock1Size], h)
				if more {
					require.Equal(t, codes.Continue, resp.code, "block1 %v", num)
					require.NotNil(t, resp.block1, "block1 %v", num)
					require.Equal(t, *encode(num, more), *resp.block1, "block1 %v", num)
					continue
				}
				// last Block1 request: the response carries the first Block2 block
				require.Equal(t, codes.Content, resp.code)
				// final response echoes the last Block1 (RFC 7959 §3.2)
				require.NotNil(t, resp.block1)
				require.Equal(t, *encode(num, false), *resp.block1)
				require.NotNil(t, resp.block2)
				require.Equal(t, *encode(0, true), *resp.block2)
				require.Equal(t, response[:testBlock1Size], resp.payload)
			}
			require.Equal(t, 1, handlerCalls)

			got := append([]byte(nil), response[:testBlock1Size]...)
			for num := int64(1); num*testBlock1Size < responseSize; num++ {
				resp := r.post(tt.tokens[requestBlocks+num-1], nil, encode(num, false), nil, h)
				require.Equal(t, codes.Content, resp.code, "block2 %v", num)
				require.Nil(t, resp.block1, "block2 %v", num)
				require.NotNil(t, resp.block2, "block2 %v", num)
				_, _, more, err := DecodeBlockOption(*resp.block2)
				require.NoError(t, err)
				require.Equal(t, (num+1)*testBlock1Size < responseSize, more, "block2 %v", num)
				got = append(got, resp.payload...)
			}
			require.Equal(t, response, got)
			require.Equal(t, 1, handlerCalls)
		})
	}
}

func TestGetRequestMatchingKey(t *testing.T) {
	cc := newTestClient()
	remote := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
	newRequest := func(code codes.Code, path string, opts ...message.Option) *pool.Message {
		req := cc.AcquireMessage(t.Context())
		t.Cleanup(func() { cc.ReleaseMessage(req) })
		req.SetCode(code)
		require.NoError(t, req.SetPath(path))
		for _, o := range opts {
			req.AddOptionBytes(o.ID, o.Value)
		}
		return req
	}
	base := getRequestMatchingKey(newRequest(codes.GET, "/a"), remote)

	// RFC 9175 §3.3: block-wise and elective NoCacheKey options (Size1, Size2) are ignored.
	require.Equal(t, base, getRequestMatchingKey(newRequest(codes.GET, "/a",
		message.Option{ID: message.Block2, Value: []byte{0x16}},
		message.Option{ID: message.Size2, Value: []byte{0x0b, 0xb8}},
		message.Option{ID: message.Size1, Value: []byte{0x0b, 0xb8}},
	), remote))

	// Everything else distinguishes requests.
	for name, req := range map[string]*pool.Message{
		"path":           newRequest(codes.GET, "/b"),
		"code":           newRequest(codes.DELETE, "/a"),
		"observe":        newRequest(codes.GET, "/a", message.Option{ID: message.Observe}),
		"content-format": newRequest(codes.GET, "/a", message.Option{ID: message.ContentFormat, Value: []byte{50}}),
		"etag":           newRequest(codes.GET, "/a", message.Option{ID: message.ETag, Value: []byte{1}}),
		"accept":         newRequest(codes.GET, "/a", message.Option{ID: message.Accept, Value: []byte{50}}),
	} {
		require.NotEqual(t, base, getRequestMatchingKey(req, remote), name)
	}
	require.NotEqual(t, base, getRequestMatchingKey(newRequest(codes.GET, "/a"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5684}), "remote")
}

func (b *block1Receiver) get(token message.Token, block2 *uint32, h func(*responsewriter.ResponseWriter[*testClient], *pool.Message)) block1Block2Exchange {
	req := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(token)
	req.SetType(message.Confirmable)
	require.NoError(b.t, req.SetPath("/big"))
	if block2 != nil {
		req.SetOptionUint32(message.Block2, *block2)
	}
	respMsg := b.cc.AcquireMessage(b.t.Context())
	respMsg.SetToken(token)
	w := responsewriter.New(respMsg, b.cc)
	defer func() { b.cc.ReleaseMessage(w.Message()) }()
	b.bw.Handle(w, req, SZX1024, 64*1024, b.remote, h)

	resp := block1Block2Exchange{code: w.Message().Code(), token: w.Message().Token()}
	require.Equal(b.t, token, resp.token)
	if v, err := w.Message().GetOptionUint32(message.Block2); err == nil {
		resp.block2 = &v
	}
	if w.Message().Body() != nil {
		body, err := w.Message().ReadBody()
		require.NoError(b.t, err)
		resp.payload = body
	}
	return resp
}

func (b *block1Receiver) notify(token message.Token, body []byte) {
	msg := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(msg)
	msg.SetCode(codes.Content)
	msg.SetToken(token)
	msg.SetObserve(2)
	msg.SetBody(bytes.NewReader(body))
	require.NoError(b.t, b.bw.WriteMessage(msg, SZX1024, 64*1024, b.remote, func(*pool.Message) error { return nil }))
}

// TestBlockWiseReceiveGetBlock2ChangingToken covers GET Block2 continuations with a new
// token per block (RFC 7959 §2.4), as libcoap does: the transfer resumes from the
// stored representation, so the handler is called once and the blocks are consistent.
func TestBlockWiseReceiveGetBlock2ChangingToken(t *testing.T) {
	encode := func(num int64) *uint32 {
		v, err := EncodeBlockOption(SZX1024, num, false)
		require.NoError(t, err)
		return &v
	}
	const size = 2*testBlock1Size + 100
	newRepresentation := func(version byte) []byte {
		body := bytes.Repeat([]byte{version}, size)
		return body
	}
	type server struct {
		r       *block1Receiver
		calls   int
		version byte
		h       func(*responsewriter.ResponseWriter[*testClient], *pool.Message)
	}
	newServer := func(t *testing.T) *server {
		s := &server{r: newBlock1Receiver(t, time.Minute), version: 1}
		s.h = func(w *responsewriter.ResponseWriter[*testClient], req *pool.Message) {
			s.calls++
			require.Equal(t, codes.GET, req.Code())
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(newRepresentation(s.version))))
		}
		return s
	}
	fetch := func(t *testing.T, s *server, tokens []message.Token) []byte {
		resp := s.r.get(tokens[0], nil, s.h)
		require.Equal(t, codes.Content, resp.code)
		got := resp.payload
		for num := int64(1); num*testBlock1Size < size; num++ {
			resp = s.r.get(tokens[num], encode(num), s.h)
			require.Equal(t, codes.Content, resp.code, "block %v", num)
			got = append(got, resp.payload...)
		}
		return got
	}

	t.Run("changing tokens", func(t *testing.T) {
		s := newServer(t)
		first := s.r.get(message.Token{0x01}, nil, s.h)
		require.Equal(t, newRepresentation(1)[:testBlock1Size], first.payload)
		// the representation changes during the transfer, the blocks stay consistent
		s.version = 2
		got := first.payload
		for num, token := range []message.Token{{0x02}, {0x03}} {
			resp := s.r.get(token, encode(int64(num+1)), s.h)
			require.Equal(t, codes.Content, resp.code)
			got = append(got, resp.payload...)
		}
		require.Equal(t, newRepresentation(1), got)
		require.Equal(t, 1, s.calls)
		// the completed transfer is removed
		require.Equal(t, 0, s.r.bw.sendingMessagesCache.Length())
	})

	t.Run("completed transfer is not resumed", func(t *testing.T) {
		s := newServer(t)
		require.Equal(t, newRepresentation(1), fetch(t, s, []message.Token{{0x01}, {0x02}, {0x03}}))
		require.Equal(t, 1, s.calls)
		s.version = 2
		resp := s.r.get(message.Token{0x04}, encode(1), s.h)
		require.Equal(t, newRepresentation(2)[testBlock1Size:2*testBlock1Size], resp.payload)
		require.Equal(t, 2, s.calls)
	})

	t.Run("ambiguous continuation regenerates", func(t *testing.T) {
		s := newServer(t)
		s.r.get(message.Token{0x01}, nil, s.h)
		s.version = 2
		s.r.get(message.Token{0x02}, nil, s.h)
		resp := s.r.get(message.Token{0x03}, encode(1), s.h)
		require.Equal(t, codes.Content, resp.code)
		require.Equal(t, newRepresentation(2)[testBlock1Size:2*testBlock1Size], resp.payload)
		require.Equal(t, 3, s.calls)
	})

	t.Run("notification invalidates continuation", func(t *testing.T) {
		s := newServer(t)
		s.r.get(message.Token{0x01}, nil, s.h)
		s.version = 2
		s.r.notify(message.Token{0x10}, newRepresentation(2))
		// RFC 7959 §3.4: the rest of the notification must come from the new representation
		resp := s.r.get(message.Token{0x02}, encode(1), s.h)
		require.Equal(t, newRepresentation(2)[testBlock1Size:2*testBlock1Size], resp.payload)
		require.Equal(t, 2, s.calls)
		// the original token still continues its own consistent transfer
		resp = s.r.get(message.Token{0x01}, encode(1), s.h)
		require.Equal(t, newRepresentation(1)[testBlock1Size:2*testBlock1Size], resp.payload)
	})

	t.Run("tokenless notification invalidates continuation", func(t *testing.T) {
		s := newServer(t)
		s.r.get(nil, nil, s.h)
		s.version = 2
		s.r.notify(nil, newRepresentation(2))
		resp := s.r.get(nil, encode(1), s.h)
		require.Equal(t, newRepresentation(2)[testBlock1Size:2*testBlock1Size], resp.payload)
		require.Equal(t, 2, s.calls)
	})
}

// TestBlockWiseReceivePostBlock2ZeroIsNewRequest checks that a POST with a new token and
// Block2 num 0 (early size negotiation, RFC 7959 §2.4) starts a new request instead of
// resuming the cached response of a previous matchable POST.
func TestBlockWiseReceivePostBlock2ZeroIsNewRequest(t *testing.T) {
	const responseSize = 2*testBlock1Size + 100
	r := newBlock1Receiver(t, time.Minute)
	handlerCalls := 0
	h := func(w *responsewriter.ResponseWriter[*testClient], req *pool.Message) {
		handlerCalls++
		require.Equal(t, codes.POST, req.Code())
		response := bytes.Repeat([]byte{byte(handlerCalls)}, responseSize)
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(response)))
	}
	block2, err := EncodeBlockOption(SZX1024, 0, false)
	require.NoError(t, err)

	first := r.post(message.Token{0x01}, nil, &block2, nil, h)
	require.Equal(t, codes.Content, first.code)
	require.Equal(t, bytes.Repeat([]byte{1}, testBlock1Size), first.payload)
	require.Equal(t, 1, handlerCalls)

	// the first response is still cached, a new request must reach the handler
	second := r.post(message.Token{0x02}, nil, &block2, nil, h)
	require.Equal(t, codes.Content, second.code)
	require.Equal(t, 2, handlerCalls)
	require.Equal(t, bytes.Repeat([]byte{2}, testBlock1Size), second.payload)
}

// TestBlockWiseReceiveBlock1ContinuationDoesNotWaitForBusyTransfer checks that matching a
// token-changed Block1 continuation does not lock other transfers, so a busy transfer
// cannot stall message processing, while a busy matching transfer is still found.
func TestBlockWiseReceiveBlock1ContinuationDoesNotWaitForBusyTransfer(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	r.token = message.Token{0x01}
	require.Equal(t, codes.Continue, r.sendPayloadWithPath(0, true, bytes.Repeat([]byte{0}, testBlock1Size), "/busy").code)
	busyKey := r.exchangeKeyWithPath("/busy")
	r.token = message.Token{0x02}
	require.Equal(t, codes.Continue, r.sendPayloadWithPath(0, true, bytes.Repeat([]byte{0}, testBlock1Size), "/next").code)
	nextKey := r.exchangeKeyWithPath("/next")

	busy := r.bw.receivingMessagesCache.Load(busyKey).Data()
	require.NoError(t, acquireMessageGuard(busy))
	defer busy.Release(1)
	next := r.bw.receivingMessagesCache.Load(nextKey).Data()
	require.NoError(t, acquireMessageGuard(next))

	resolve := func(path string) uint64 {
		req := r.cc.AcquireMessage(t.Context())
		defer r.cc.ReleaseMessage(req)
		req.SetCode(codes.PUT)
		req.SetToken(message.Token{0x03})
		require.NoError(t, req.SetPath(path))
		block, err := EncodeBlockOption(SZX1024, 1, false)
		require.NoError(t, err)
		req.SetOptionUint32(message.Block1, block)
		key, err := getMessageExchangeKey(req, r.remote)
		require.NoError(t, err)
		done := make(chan uint64, 1)
		go func() {
			resolved, errR := r.bw.resolveBlock1ExchangeKey(req, r.remote, key)
			assert.NoError(t, errR)
			done <- resolved
		}()
		select {
		case resolved := <-done:
			return resolved
		case <-time.After(time.Second):
			require.FailNow(t, "resolveBlock1ExchangeKey waits for a busy transfer")
			return 0
		}
	}
	// the matching transfer is busy, e.g. still processing a retransmission
	require.Equal(t, nextKey, resolve("/next"))
	next.Release(1)
	require.Equal(t, nextKey, resolve("/next"))

	r.token = message.Token{0x03}
	resp := r.sendPayloadWithPath(1, false, bytes.Repeat([]byte{1}, testBlock1Size), "/next")
	require.Equal(t, codes.Changed, resp.code)
	require.Len(t, r.delivered, 1)
	require.Len(t, r.delivered[0], 2*testBlock1Size)
}
