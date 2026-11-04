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

type qObservationReadHook struct {
	*bytes.Reader
	beforeRead func()
}

func (r *qObservationReadHook) Read(p []byte) (int, error) {
	if r.beforeRead != nil {
		hook := r.beforeRead
		r.beforeRead = nil
		hook()
	}
	return r.Reader.Read(p)
}

func TestQBlockObserveCancellationDuringNotificationSnapshot(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "replace"}[replace], func(t *testing.T) {
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
			notification.SetBody(&qObservationReadHook{
				Reader: bytes.NewReader(bytes.Repeat([]byte{7}, 48)),
				beforeRead: func() {
					b.CancelQBlockObservation(request.Token(), nil)
					if replace {
						require.NoError(t, b.registerQObservation(request, nil, SZX16))
					}
				},
			})
			handled, err := b.WriteNotification(notification, nil)
			require.True(t, handled)
			require.ErrorIs(t, err, context.Canceled)
			require.Empty(t, b.q2Senders)
			require.Empty(t, cc.jobs)
			require.Empty(t, cc.sent)
			if replace {
				require.Len(t, b.qObservations, 1, "an older writer must not retire a new registration")
			} else {
				require.Empty(t, b.qObservations)
			}
			b.CloseQBlockTransfers()
			require.Equal(t, bytesBefore, qRetainedBytes.Load())
			require.Equal(t, transfersBefore, qRetainedTransfers.Load())
		})
	}
}

func TestQBlockObserveRegistrationTokenTerminalDuringPartialBody(t *testing.T) {
	for _, code := range []codes.Code{codes.Content, codes.BadOption, codes.NotFound} {
		t.Run(code.String(), func(t *testing.T) {
			bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
			cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
			b := New(cc, nil)
			defer b.CloseQBlockTransfers()
			request := pool.NewMessage(context.Background())
			request.SetCode(codes.GET)
			request.SetToken([]byte("observe"))
			request.SetObserve(0)
			sequence := uint32(10)
			terminal := pool.NewMessage(context.Background())
			terminal.SetCode(code)
			terminal.SetToken(request.Token())
			terminal.SetBody(bytes.NewReader([]byte("finished")))
			exchange := &qObserveExchange{messages: []*pool.Message{terminal}}
			response, err := b.ReceiveQBlock2(request, SZX16, exchange,
				qObservePayload(t, SZX16, 0, 48, 1, &sequence))
			require.NoError(t, err)
			defer cc.ReleaseMessage(response)
			require.Equal(t, code, response.Code())
			require.False(t, response.HasOption(message.Observe))
			body, err := response.ReadBody()
			require.NoError(t, err)
			require.Equal(t, []byte("finished"), body)
			require.Equal(t, bytesBefore, qRetainedBytes.Load())
			require.Equal(t, transfersBefore, qRetainedTransfers.Load())
		})
	}
}

func TestQBlockObserveRecoveryIsNotRegistrationTerminal(t *testing.T) {
	for _, code := range []codes.Code{codes.Content, codes.BadOption} {
		t.Run(code.String(), func(t *testing.T) {
			cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
			b := New(cc, nil)
			defer b.CloseQBlockTransfers()
			request := pool.NewMessage(context.Background())
			request.SetCode(codes.GET)
			request.SetToken([]byte("observe"))
			request.SetObserve(0)
			sequence := uint32(10)
			response := pool.NewMessage(context.Background())
			response.SetCode(code)
			response.SetToken([]byte("recovery"))
			exchange := &qObserveExchange{messages: []*pool.Message{response}}
			got, err := b.ReceiveQBlock2(request, SZX16, exchange,
				qObservePayload(t, SZX16, 0, 48, 1, &sequence))
			require.Nil(t, got)
			require.ErrorIs(t, err, ErrInvalidOptionBlock2)
		})
	}
}

func TestQBlockObserveMultiblockTerminalReplacesPartialBody(t *testing.T) {
	for _, code := range []codes.Code{codes.Content, codes.BadOption, codes.NotFound} {
		t.Run(code.String(), func(t *testing.T) {
			cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
			b := New(cc, nil)
			defer b.CloseQBlockTransfers()
			request := pool.NewMessage(context.Background())
			request.SetCode(codes.GET)
			request.SetToken([]byte("observe"))
			request.SetObserve(0)
			sequence := uint32(10)
			exchange := new(qObserveExchange)
			for num := int64(0); num < 3; num++ {
				part := qObservePayload(t, SZX16, num, 48, 7, nil)
				part.SetCode(code)
				if num != 1 {
					part.SetToken(request.Token())
				}
				exchange.messages = append(exchange.messages, part)
				if num == 0 {
					exchange.messages = append(exchange.messages,
						qObservePayload(t, SZX16, 1, 32, 1, &sequence),
						qObservePayload(t, SZX16, 1, 32, 1, nil))
				}
			}
			response, err := b.ReceiveQBlock2(request, SZX16, exchange,
				qObservePayload(t, SZX16, 0, 32, 1, &sequence))
			require.NoError(t, err)
			defer cc.ReleaseMessage(response)
			require.Equal(t, code, response.Code())
			require.False(t, response.HasOption(message.Observe))
			require.False(t, response.HasOption(message.QBlock2))
			body, err := response.ReadBody()
			require.NoError(t, err)
			require.Equal(t, bytes.Repeat([]byte{7}, 48), body)
		})
	}
}

func TestQBlockObserveTerminalWithUnchangedRepresentation(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("observe"))
	request.SetObserve(0)
	sequence := uint32(10)
	terminal := qObservePayload(t, SZX16, 0, 48, 1, nil)
	terminal.SetToken(request.Token())
	exchange := &qObserveExchange{messages: []*pool.Message{
		terminal,
		qObservePayload(t, SZX16, 1, 48, 1, nil),
		qObservePayload(t, SZX16, 2, 48, 1, &sequence),
		qObservePayload(t, SZX16, 2, 48, 1, nil),
	}}
	response, err := b.ReceiveQBlock2(request, SZX16, exchange,
		qObservePayload(t, SZX16, 0, 48, 1, &sequence))
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.False(t, response.HasOption(message.Observe), "an unchanged final body still terminates Observe")
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 48), body)
}

func TestQBlockObserveLaterRegistrationPayloadMayOmitObserve(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte("observe"))
	request.SetObserve(0)
	sequence := uint32(10)
	exchange := new(qObserveExchange)
	for num := int64(1); num < 3; num++ {
		part := qObservePayload(t, SZX16, num, 48, 1, nil)
		part.SetToken(request.Token())
		exchange.messages = append(exchange.messages, part)
	}
	response, err := b.ReceiveQBlock2(request, SZX16, exchange,
		qObservePayload(t, SZX16, 0, 48, 1, &sequence))
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	gotSequence, err := response.Observe()
	require.NoError(t, err)
	require.Equal(t, sequence, gotSequence, "later NUMs without Observe do not terminate the registration")
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{1}, 48), body)
}
