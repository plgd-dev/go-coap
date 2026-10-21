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

const testBlock1Size = 1024

type block1Receiver struct {
	t         *testing.T
	cc        *testClient
	bw        *BlockWise[*testClient]
	delivered [][]byte
}

func newBlock1Receiver(t *testing.T, expiration time.Duration) *block1Receiver {
	cc := newTestClient()
	return &block1Receiver{
		t:  t,
		cc: cc,
		bw: New(cc, expiration, func(error) {}, nil),
	}
}

type block1Response struct {
	code  codes.Code
	size1 uint32
}

func (b *block1Receiver) send(num int64, more bool, size1 ...uint32) block1Response {
	return b.sendPayload(num, more, bytes.Repeat([]byte{byte(num)}, testBlock1Size), size1...)
}

func (b *block1Receiver) sendPayload(num int64, more bool, payload []byte, size1 ...uint32) block1Response {
	req := b.cc.AcquireMessage(b.t.Context())
	defer b.cc.ReleaseMessage(req)
	req.SetCode(codes.PUT)
	req.SetToken(message.Token{0x01})
	req.SetType(message.Confirmable)
	block, err := EncodeBlockOption(SZX1024, num, more)
	require.NoError(b.t, err)
	req.SetOptionUint32(message.Block1, block)
	for _, v := range size1 {
		req.SetOptionUint32(message.Size1, v)
	}
	req.SetBody(bytes.NewReader(payload))

	w := responsewriter.New(b.cc.AcquireMessage(b.t.Context()), b.cc)
	defer func() { b.cc.ReleaseMessage(w.Message()) }()
	b.bw.Handle(w, req, SZX1024, 64*1024, func(w *responsewriter.ResponseWriter[*testClient], r *pool.Message) {
		body, errR := r.ReadBody()
		require.NoError(b.t, errR)
		b.delivered = append(b.delivered, body)
		require.NoError(b.t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	})
	respSize1, _ := w.Message().GetOptionUint32(message.Size1)
	return block1Response{code: w.Message().Code(), size1: respSize1}
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
	const (
		expiration = 100 * time.Millisecond
		interval   = 40 * time.Millisecond
		blocks     = 8 // total duration exceeds expiration
	)
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
			r := newBlock1Receiver(t, expiration)
			r.bw.SetRollingExpiration(tt.rolling)
			var got codes.Code
			for num := int64(0); num < blocks; num++ {
				got = r.send(num, num != blocks-1).code
				if got != codes.Continue {
					break
				}
				time.Sleep(interval)
				r.bw.CheckExpirations(time.Now())
			}
			require.Equal(t, tt.wantLast, got)
			if tt.rolling {
				require.Len(t, r.delivered, 1)
				require.Len(t, r.delivered[0], blocks*testBlock1Size)
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
	e := r.bw.receivingMessagesCache.Load(message.Token{0x01}.Hash())
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
			require.Nil(t, r.bw.receivingMessagesCache.Load(message.Token{0x01}.Hash()))
		})
		t.Run(name+" next block", func(t *testing.T) {
			r := newBlock1Receiver(t, time.Minute)
			r.bw.SetRollingExpiration(true)
			r.sendBlocks(2)
			require.Equal(t, codes.RequestEntityIncomplete, r.sendPayload(2, true, payload).code)
			require.Nil(t, r.bw.receivingMessagesCache.Load(message.Token{0x01}.Hash()), "state must be discarded")
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
