package qblock

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

type q1RecoveryTestExchange struct {
	nums []int64
}

func (*q1RecoveryTestExchange) NewToken() (message.Token, error) {
	return message.Token("recovery"), nil
}

func (e *q1RecoveryTestExchange) Send(msg *pool.Message) error {
	value, err := msg.GetOptionUint32(message.QBlock1)
	if err != nil {
		return err
	}
	_, num, _, err := DecodeQBlockOption(value)
	if err != nil {
		return err
	}
	e.nums = append(e.nums, num)
	return nil
}

func (*q1RecoveryTestExchange) Receive(context.Context) (*pool.Message, error) {
	return nil, errors.New("not used")
}

func (*q1RecoveryTestExchange) Close() {}

type q1IncompleteTestExchange struct {
	responses []*pool.Message
	sent      int
}

func (*q1IncompleteTestExchange) NewToken() (message.Token, error) {
	return message.Token("recovery"), nil
}

func (e *q1IncompleteTestExchange) Send(*pool.Message) error {
	e.sent++
	return nil
}

func (e *q1IncompleteTestExchange) Receive(context.Context) (*pool.Message, error) {
	if len(e.responses) == 0 {
		return nil, errors.New("no test response")
	}
	response := e.responses[0]
	e.responses = e.responses[1:]
	return response, nil
}

func (*q1IncompleteTestExchange) Close() {}

type q1IncompleteTestTransport struct {
	*qTestTransport
	exchange *q1IncompleteTestExchange
}

func (c *q1IncompleteTestTransport) OpenQBlockExchange(context.Context, message.Token) (Exchange, error) {
	return c.exchange, nil
}

func TestQBlock1MissingReportRecoversEveryReportedSet(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.POST)
	request.SetToken([]byte("upload"))
	body := bytes.Repeat([]byte{3}, 30*int(SZX16.Size()))
	request.SetBody(bytes.NewReader(body))
	require.NoError(t, request.SetRequestTag([]byte{1}))
	payloadSender, err := NewSender(bytes.NewReader(body), SZX16, int64(len(body)))
	require.NoError(t, err)
	exchange := &q1RecoveryTestExchange{}
	sender := &q1BodySender[*qTestTransport]{
		b: b, request: request, sender: payloadSender, exchange: exchange, tag: []byte{1},
		size: uint32(len(body)), szx: SZX16, blockCount: 30, attempts: make(map[int64]int),
	}
	report, err := EncodeMissingBlocks(nil, []uint32{1, 11, 22})
	require.NoError(t, err)
	response := cc.AcquireMessage(context.Background())
	response.SetCode(codes.RequestEntityIncomplete)
	response.SetContentFormat(message.AppMissingBlocksCBORSeq)
	response.SetBody(bytes.NewReader(report))

	require.NoError(t, sender.missing(response))
	require.Equal(t, []int64{1, 11, 22}, exchange.nums)
	require.Equal(t, []int64{1, 11, 22}, sender.pendingRecovery)

	exchange.nums = nil
	report, err = EncodeMissingBlocks(nil, []uint32{1, 30})
	require.NoError(t, err)
	response = cc.AcquireMessage(context.Background())
	response.SetCode(codes.RequestEntityIncomplete)
	response.SetContentFormat(message.AppMissingBlocksCBORSeq)
	response.SetBody(bytes.NewReader(report))
	require.Error(t, sender.missing(response))
	require.Empty(t, exchange.nums, "reject the whole report before sending a valid prefix")
}

func TestQBlock1NonCBORMissingResponseIsReturnedToCaller(t *testing.T) {
	missing := pool.NewMessage(context.Background())
	missing.SetCode(codes.RequestEntityIncomplete)
	missing.SetContentFormat(message.TextPlain)
	missing.SetBody(bytes.NewReader([]byte("upload incomplete")))
	exchange := &q1IncompleteTestExchange{responses: []*pool.Message{missing}}
	cc := &q1IncompleteTestTransport{
		qTestTransport: &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)},
		exchange:       exchange,
	}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.POST)
	request.SetType(message.Confirmable)
	request.SetToken([]byte("upload"))
	request.SetBody(bytes.NewReader([]byte("payload")))

	response, err := b.DoQBlock1(request, SZX16, 1024, nil)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.RequestEntityIncomplete, response.Code())
	require.Equal(t, request.Token(), response.Token())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("upload incomplete"), body)
	require.Equal(t, 1, exchange.sent, "a non-CBOR 4.08 is not interpreted as a selective recovery list")
}

func TestQBlock1FinalBadOptionWithQBlock2Body(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.POST)
	request.SetToken([]byte("upload"))
	require.NoError(t, request.SetRequestTag([]byte{1}))
	sender := &q1BodySender[*qTestTransport]{
		b: b, request: request, tag: []byte{1}, size: 8, szx: SZX16,
	}
	response := qObservePayload(t, SZX16, 0, 8, 3, nil)
	response.SetCode(codes.BadOption)

	got, err := sender.final(response)
	require.NoError(t, err)
	defer cc.ReleaseMessage(got)
	require.Equal(t, codes.BadOption, got.Code())
	require.Equal(t, request.Token(), got.Token())
	require.False(t, got.HasOption(message.QBlock2))
	body, err := got.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{3}, 8), body)
}

func TestQBlock1FinalBadOptionWithoutQBlock2IsUnsupported(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.POST)
	request.SetToken([]byte("upload"))
	response := cc.AcquireMessage(context.Background())
	response.SetCode(codes.BadOption)

	sender := &q1BodySender[*qTestTransport]{b: b, request: request}
	got, err := sender.final(response)
	require.Nil(t, got)
	require.ErrorIs(t, err, ErrQBlockUnsupported)
}
