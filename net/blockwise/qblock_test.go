package blockwise_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestQBlockCompatibilityTypesAndErrors(t *testing.T) {
	szx := qblock.SZX1024
	mode := qblock.QBlockwiseAuto
	require.NoError(t, blockwise.ValidateQBlockwiseConfig(mode, true))
	require.Equal(t, []blockwise.SZX{blockwise.SZX1024}, []qblock.SZX{szx})
	require.Equal(t, int64(1024), szx.Size())
	for value := 0; value <= 255; value++ {
		szx := blockwise.SZX(value)
		want := int64(-1)
		if value <= 7 {
			want = int64(16) << min(value, 6)
		}
		require.Equal(t, want, szx.Size())
	}
	pairs := [][2]error{
		{blockwise.ErrBlockNumberExceedLimit, qblock.ErrBlockNumberExceedLimit},
		{blockwise.ErrBlockInvalidSize, qblock.ErrBlockInvalidSize},
		{blockwise.ErrInvalidOptionBlock1, qblock.ErrInvalidOptionBlock1},
		{blockwise.ErrInvalidOptionBlock2, qblock.ErrInvalidOptionBlock2},
		{blockwise.ErrInvalidSZX, qblock.ErrInvalidSZX},
		{blockwise.ErrRequestEntityTooLarge, qblock.ErrRequestEntityTooLarge},
		{blockwise.ErrInvalidMissingBlocks, qblock.ErrInvalidMissingBlocks},
		{blockwise.ErrQBlockInvalidSize, qblock.ErrQBlockInvalidSize},
		{blockwise.ErrQBlockInvalidNUM, qblock.ErrQBlockInvalidNUM},
		{blockwise.ErrQBlockIncomplete, qblock.ErrQBlockIncomplete},
		{blockwise.ErrQBlockConflict, qblock.ErrQBlockConflict},
		{blockwise.ErrQBlockTooLarge, qblock.ErrQBlockTooLarge},
		{blockwise.ErrQBlockScheduleFull, qblock.ErrQBlockScheduleFull},
		{blockwise.ErrQBlockInvalidMode, qblock.ErrQBlockInvalidMode},
		{blockwise.ErrQBlockNeedsBlockwise, qblock.ErrQBlockNeedsBlockwise},
		{blockwise.ErrQBlockDisabled, qblock.ErrQBlockDisabled},
		{blockwise.ErrQBlockUnsupported, qblock.ErrQBlockUnsupported},
	}
	for _, pair := range pairs {
		require.ErrorIs(t, pair[0], pair[1], "aliases must preserve sentinel identity")
	}
}

func TestQBlockCompatibilityCodecsAndBodies(t *testing.T) {
	value, err := blockwise.EncodeQBlockOption(blockwise.SZX16, 0xfffff, true)
	require.NoError(t, err)
	szx, num, more, err := qblock.DecodeQBlockOption(value)
	require.NoError(t, err)
	require.Equal(t, qblock.SZX16, szx)
	require.EqualValues(t, 0xfffff, num)
	require.True(t, more)
	szx, num, more, err = blockwise.DecodeQBlockOption(1 << 24)
	require.ErrorIs(t, err, qblock.ErrBlockInvalidSize)
	require.Zero(t, szx)
	require.Zero(t, num)
	require.False(t, more)
	encoded, err := blockwise.EncodeMissingBlocks(nil, []uint32{1, 24, 65536})
	require.NoError(t, err)
	missing, err := blockwise.DecodeMissingBlocks(nil, encoded)
	require.NoError(t, err)
	require.Equal(t, []uint32{1, 24, 65536}, missing)

	var assembly *qblock.Assembly
	assembly, err = blockwise.NewQBlockAssembly(blockwise.SZX16, 17, 32, 2)
	require.NoError(t, err)
	complete, _, err := assembly.Add(1, false, []byte{2})
	require.NoError(t, err)
	require.False(t, complete)
	complete, _, err = assembly.Add(0, true, bytes.Repeat([]byte{1}, 16))
	require.NoError(t, err)
	require.True(t, complete)
	body, err := assembly.ReadSeeker()
	require.NoError(t, err)
	payload, err := io.ReadAll(body)
	require.NoError(t, err)

	var sender *qblock.Sender
	sender, err = blockwise.NewQBlockSender(bytes.NewReader(payload), blockwise.SZX16, 17)
	require.NoError(t, err)
	block, more, err := sender.Block(1)
	require.NoError(t, err)
	require.Equal(t, []byte{2}, block)
	require.False(t, more)
}

type qCompatibilityClient struct {
	p *pool.Pool
}

func (c qCompatibilityClient) AcquireMessage(ctx context.Context) *pool.Message {
	return c.p.AcquireMessage(ctx)
}

func (c qCompatibilityClient) ReleaseMessage(msg *pool.Message) {
	c.p.ReleaseMessage(msg)
}

func TestQBlockCompatibilityLimitsAndExpiration(t *testing.T) {
	cc := qCompatibilityClient{p: pool.New(16, 1024)}
	b := blockwise.New(cc, time.Second, func(error) {}, nil)
	defer b.CloseQBlockTransfers()
	send := func(tag byte) codes.Code {
		t.Helper()
		r := cc.AcquireMessage(context.Background())
		defer cc.ReleaseMessage(r)
		r.SetCode(codes.PUT)
		r.SetType(message.Confirmable)
		r.SetToken([]byte{tag})
		require.NoError(t, r.SetRequestTag([]byte{tag}))
		require.NoError(t, r.SetPath("/q"))
		option, err := blockwise.EncodeQBlockOption(blockwise.SZX16, 0, true)
		require.NoError(t, err)
		r.SetOptionUint32(message.QBlock1, option)
		r.SetOptionUint32(message.Size1, 32)
		r.SetBody(bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
		w := responsewriter.New(cc.AcquireMessage(r.Context()), cc)
		b.Handle(w, r, blockwise.SZX16, 1024, nil,
			func(*responsewriter.ResponseWriter[qCompatibilityClient], *pool.Message) {
				t.Error("partial bodies must not reach the application")
			})
		defer cc.ReleaseMessage(w.Message())
		return w.Message().Code()
	}
	b.SetMaxReceiveBodySize(16)
	require.Equal(t, codes.RequestEntityTooLarge, send(1))
	b.SetMaxReceiveBodySize(32)
	b.SetReceivingMessagesCacheMaxEntries(1)
	require.Equal(t, codes.Continue, send(2))
	require.Equal(t, codes.ServiceUnavailable, send(3))
	b.CheckExpirations(time.Now().Add(248 * time.Second))
	require.Equal(t, codes.Continue, send(4), "expiration must reach the extracted engine")
}
