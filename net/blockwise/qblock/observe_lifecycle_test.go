package qblock

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestQBlockObserveTerminalWriteCancelsRegistrationAndBody(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()

	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("observe"))
	request.SetObserve(0)
	require.NoError(t, b.registerQObservation(request, nil, SZX16))

	notification := pool.NewMessage(context.Background())
	notification.SetCode(codes.Content)
	notification.SetToken(request.Token())
	notification.SetObserve(10)
	notification.SetBody(bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	handled, err := b.WriteNotification(notification, nil)
	require.True(t, handled)
	require.NoError(t, err)
	require.Len(t, b.q2Senders, 1)
	require.Len(t, cc.jobs, 1)

	terminal := pool.NewMessage(context.Background())
	terminal.SetCode(codes.NotFound)
	terminal.SetToken(request.Token())
	handled, err = b.WriteNotification(terminal, nil)
	require.False(t, handled, "the terminal message should use the ordinary writer")
	require.NoError(t, err)
	require.Empty(t, b.qObservations)
	require.Empty(t, b.q2Senders)
	require.Empty(t, cc.jobs, "a pending Q body must be canceled before fallthrough")

	for i := range 128 {
		request.SetToken([]byte{byte(i), byte(i >> 8)})
		require.NoError(t, b.registerQObservation(request, nil, SZX16))
		terminal.SetToken(request.Token())
		handled, err = b.WriteNotification(terminal, nil)
		require.False(t, handled)
		require.NoError(t, err)
	}
	require.Empty(t, b.qObservations, "repeated terminations must not consume registration capacity")
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockObserveNotificationContinuesAfterSinglePayloadSet(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	b.config.MaxPayloads = 1
	defer b.CloseQBlockTransfers()

	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("single-payload-sets"))
	request.SetObserve(0)
	require.NoError(t, b.registerQObservation(request, nil, SZX16))

	notification := pool.NewMessage(context.Background())
	notification.SetCode(codes.Content)
	notification.SetType(message.NonConfirmable)
	notification.SetToken(request.Token())
	notification.SetObserve(10)
	notification.SetBody(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	handled, err := b.WriteNotification(notification, nil)
	require.True(t, handled)
	require.NoError(t, err)
	require.Len(t, cc.sent, 1)
	require.Len(t, cc.jobs, 1)
	for key, job := range cc.jobs {
		require.Greater(t, job.deadline, time.Now(), "NON next-set continuation keeps the configured set delay")
		delete(cc.jobs, key)
		require.NoError(t, job.run())
	}

	require.Len(t, cc.sent, 2)
	value, err := cc.sent[1].GetOptionUint32(message.QBlock2)
	require.NoError(t, err)
	_, num, _, err := DecodeQBlockOption(value)
	require.NoError(t, err)
	require.EqualValues(t, 1, num)
}
