package qblock

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

type qObserveExchange struct {
	messages []*pool.Message
	cancel   context.CancelFunc
	sent     []uint32
}

func (*qObserveExchange) NewToken() (message.Token, error) { return []byte("recovery"), nil }
func (e *qObserveExchange) Send(request *pool.Message) error {
	value, err := request.GetOptionUint32(message.QBlock2)
	if err != nil {
		return err
	}
	e.sent = append(e.sent, value)
	return nil
}
func (*qObserveExchange) Close() {}
func (e *qObserveExchange) Receive(ctx context.Context) (*pool.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(e.messages) == 0 {
		if e.cancel != nil {
			e.cancel()
		}
		return nil, context.Canceled
	}
	response := e.messages[0]
	e.messages = e.messages[1:]
	return response, nil
}

func TestQBlockConfirmableRecoveryResumesBody(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken([]byte("original"))
	req.SetType(message.Confirmable)
	assembly, err := NewAssembly(SZX16, 321, 1024, 100)
	require.NoError(t, err)
	for num := int64(0); num < b.config.MaxPayloads; num++ {
		_, _, err = assembly.Add(num, true, bytes.Repeat([]byte{7}, 16))
		require.NoError(t, err)
	}
	exchange := new(qObserveExchange)
	receiver := q2BodyReceiver[*qTestTransport]{
		b: b, req: req, exchange: exchange, assembly: assembly, szx: SZX16, maxSZX: SZX16,
		requestedSet: 0, retries: 1, sentRequests: 1,
	}
	require.NoError(t, receiver.advance(9))
	require.Len(t, exchange.sent, 1)
	szx, num, more, err := DecodeQBlockOption(exchange.sent[0])
	require.NoError(t, err)
	require.Equal(t, SZX16, szx)
	require.EqualValues(t, 10, num)
	require.True(t, more, "resume the body, not another isolated missing-payload request")
	require.Zero(t, receiver.retries)
	require.EqualValues(t, -1, receiver.requestedSet)
	require.NoError(t, receiver.advance(9))
	require.Len(t, exchange.sent, 1, "duplicate progress cannot repeat a completed-set continuation")
}

func qObservePayload(t *testing.T, szx SZX, num int64, size int, value byte, sequence *uint32) *pool.Message {
	t.Helper()
	response := pool.NewMessage(context.Background())
	response.SetCode(codes.Content)
	response.SetToken([]byte("observe"))
	response.SetType(message.NonConfirmable)
	more := (num+1)*szx.Size() < int64(size)
	option, err := EncodeQBlockOption(szx, num, more)
	require.NoError(t, err)
	response.SetOptionUint32(message.QBlock2, option)
	response.SetOptionUint32(message.Size2, uint32(size))
	response.SetOptionBytes(message.ETag, []byte{value})
	if sequence != nil {
		response.SetObserve(*sequence)
	}
	response.SetBody(bytes.NewReader(bytes.Repeat([]byte{value}, int(min(szx.Size(), int64(size)-num*szx.Size())))))
	return response
}

func TestQBlockObservePartialReplacement(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken([]byte("observe"))
	req.SetType(message.Confirmable)
	req.SetObserve(0)
	oldSequence, newSequence := uint32(10), uint32(11)
	exchange := &qObserveExchange{messages: []*pool.Message{
		qObservePayload(t, SZX16, 1, 48, 2, &newSequence),
		qObservePayload(t, SZX16, 1, 32, 1, &oldSequence),
		qObservePayload(t, SZX16, 0, 48, 2, &newSequence),
		qObservePayload(t, SZX16, 2, 48, 2, nil),
	}}
	response, err := b.ReceiveQBlock2(req, SZX16, exchange, qObservePayload(t, SZX16, 0, 32, 1, &oldSequence))
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{2}, 48), body)
	sequence, err := response.Observe()
	require.NoError(t, err)
	require.Equal(t, newSequence, sequence, "a recovery's omitted Observe must not lose notification ordering")
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockObserveTerminalNotificationDuringPartialBody(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken([]byte("observe"))
	req.SetObserve(0)
	sequence := uint32(10)
	terminal := pool.NewMessage(context.Background())
	terminal.SetCode(codes.NotFound)
	terminal.SetToken([]byte("recovery"))
	exchange := &qObserveExchange{messages: []*pool.Message{terminal}}

	response, err := b.ReceiveQBlock2(req, SZX16, exchange,
		qObservePayload(t, SZX16, 0, 48, 1, &sequence))
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.NotFound, response.Code())
	require.Equal(t, message.Token("observe"), response.Token(),
		"the terminal notification must route through the registration token")
	require.False(t, response.HasOption(message.Observe))
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockObserveZeroMaxAgeAllowsMultiblockBody(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken([]byte("observe"))
	req.SetObserve(0)
	sequence := uint32(10)
	first := qObservePayload(t, SZX16, 0, 48, 1, &sequence)
	first.SetOptionUint32(message.MaxAge, 0)
	exchange := &qObserveExchange{messages: []*pool.Message{
		qObservePayload(t, SZX16, 1, 48, 1, nil),
		qObservePayload(t, SZX16, 2, 48, 1, nil),
	}}

	response, err := b.ReceiveQBlock2(req, SZX16, exchange, first)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 48), body)
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockObservePartialCancellationBudget(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := pool.NewMessage(ctx)
	req.SetCode(codes.GET)
	req.SetToken([]byte("observe"))
	req.SetObserve(0)
	sequence := uint32(10)
	exchange := &qObserveExchange{cancel: cancel}
	response, err := b.ReceiveQBlock2(req, SZX1024, exchange,
		qObservePayload(t, SZX1024, 0, 1<<20, 1, &sequence))
	require.Nil(t, response)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockAssemblyRetainedPayloadBudget(t *testing.T) {
	const bodySize = 1 << 20
	for _, szx := range []SZX{SZX16, SZX128, SZX1024} {
		t.Run(strconv.FormatInt(szx.Size(), 10), func(t *testing.T) {
			assembly, err := NewAssembly(szx, bodySize, bodySize, 65536)
			require.NoError(t, err)
			payload := bytes.Repeat([]byte{3}, int(szx.Size()))
			count := int64(bodySize) / szx.Size()
			for num := count - 1; num >= 0; num-- {
				_, duplicate, addErr := assembly.Add(num, num != count-1, payload)
				require.NoError(t, addErr)
				require.False(t, duplicate)
				_, duplicate, addErr = assembly.Add(num, num != count-1, payload)
				require.NoError(t, addErr)
				require.True(t, duplicate)
			}
			var capacity int
			for _, block := range assembly.blocks {
				capacity += cap(block)
			}
			require.LessOrEqual(t, capacity, bodySize*110/100+64<<10,
				"retained payload capacity excludes explicit sparse map/slice metadata")
			reader, err := assembly.ReadSeeker()
			require.NoError(t, err)
			sparse, ok := reader.(*qSparseReader)
			require.True(t, ok, "completion must not materialize a second contiguous body")
			for num, block := range assembly.blocks {
				require.Same(t, &block[0], &sparse.blocks[num][0])
			}
			read, err := io.Copy(io.Discard, reader)
			require.NoError(t, err)
			require.EqualValues(t, bodySize, read)
		})
	}
}
