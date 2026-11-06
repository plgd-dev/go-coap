package qblock

import (
	"bytes"
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

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
