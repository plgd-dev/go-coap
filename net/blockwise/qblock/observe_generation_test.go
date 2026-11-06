package qblock

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type qObservationReadHook struct {
	*bytes.Reader
	beforeRead func()
}

type blockingQBlockTransport struct {
	*qTestTransport
	mu           sync.Mutex
	sendCalls    int
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

type observeCancelLockTransport struct {
	*qTestTransport
	engine      *Engine[*observeCancelLockTransport]
	lockWasFree chan bool
}

func (c *observeCancelLockTransport) CancelQBlock(key string) {
	lockWasFree := c.engine.q2SendMu.TryLock()
	if lockWasFree {
		c.engine.q2SendMu.Unlock()
	}
	c.lockWasFree <- lockWasFree
	c.qTestTransport.CancelQBlock(key)
}

func (c *blockingQBlockTransport) SendQBlock(msg *pool.Message) error {
	c.mu.Lock()
	c.sendCalls++
	call := c.sendCalls
	c.mu.Unlock()
	if call == 1 {
		close(c.firstStarted)
		<-c.releaseFirst
	}
	return c.qTestTransport.SendQBlock(msg)
}

func (r *qObservationReadHook) Read(p []byte) (int, error) {
	if r.beforeRead != nil {
		hook := r.beforeRead
		r.beforeRead = nil
		hook()
	}
	return r.Reader.Read(p)
}

func TestQBlockFreshObserveRegistrationInvalidatesTerminalBody(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte("observe-reuse"))
	request.SetObserve(0)
	require.NoError(t, request.SetPath("/observe"))
	require.NoError(t, request.SetRequestTag([]byte("same-tag")))
	require.NoError(t, b.registerQObservation(request, nil, SZX16))

	terminal := pool.NewMessage(context.Background())
	terminal.SetCode(codes.NotFound)
	terminal.SetType(message.NonConfirmable)
	terminal.SetToken(request.Token())
	terminal.SetBody(bytes.NewReader(bytes.Repeat([]byte{1}, 48)))
	handled, err := b.WriteNotification(terminal, nil)
	require.NoError(t, err)
	require.True(t, handled)
	key := q2TransferKey(request, nil)
	require.Empty(t, b.qObservations)
	cachedTerminal, exists := b.q2Senders[key]
	require.True(t, exists)
	require.NotEmpty(t, cachedTerminal.terminalObservationKey)

	fresh := pool.NewMessage(context.Background())
	fresh.SetCode(codes.GET)
	fresh.SetType(message.NonConfirmable)
	fresh.SetToken(request.Token())
	fresh.SetObserve(0)
	require.NoError(t, fresh.SetPath("/observe"))
	require.NoError(t, fresh.SetRequestTag([]byte("same-tag")))
	value, err := EncodeQBlockOption(SZX16, 0, true)
	require.NoError(t, err)
	fresh.SetOptionUint32(message.QBlock2, value)
	var handlerCalls int
	writer := responsewriter.New(cc.AcquireMessage(fresh.Context()), cc)
	defer cc.ReleaseMessage(writer.Message())
	handler := func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
		handlerCalls++
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("fresh body"))))
		w.Message().SetObserve(20)
	}
	require.NoError(t, b.handleQBlock2(writer, fresh, SZX16, nil, handler))
	require.Equal(t, 1, handlerCalls, "the fresh Observe request must reach the handler")
	require.Equal(t, codes.Content, writer.Message().Code())
	require.True(t, writer.Message().HasOption(message.Observe))
	body, err := writer.Message().ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("fresh body"), body)
	require.Len(t, cc.sent, 1, "the terminal set was sent only before replacement")
	require.Len(t, b.qObservations, 1)
	require.NotEqual(t, codes.NotFound, b.q2Senders[key].message.Code())
	require.Empty(t, cc.jobs, "the terminal sender's scheduled transfer must be canceled")
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
						replacement := pool.NewMessage(context.Background())
						replacement.SetCode(codes.Content)
						replacement.SetType(message.NonConfirmable)
						replacement.SetToken(request.Token())
						replacement.SetObserve(11)
						replacement.SetBody(bytes.NewReader(bytes.Repeat([]byte{9}, 48)))
						handled, replacementErr := b.WriteNotification(replacement, nil)
						require.NoError(t, replacementErr)
						require.True(t, handled)
					}
				},
			})
			handled, err := b.WriteNotification(notification, nil)
			require.True(t, handled)
			require.ErrorIs(t, err, context.Canceled)
			if replace {
				require.Len(t, b.q2Senders, 1)
				require.Contains(t, cc.jobs, "q2:"+q2TransferKey(request, nil))
				require.Len(t, cc.sent, 1)
				body, readErr := b.q2Senders[q2TransferKey(request, nil)].message.ReadBody()
				require.NoError(t, readErr)
				require.Equal(t, bytes.Repeat([]byte{9}, 48), body)
				require.Len(t, b.qObservations, 1, "an older writer must not retire a new registration")
			} else {
				require.Empty(t, b.q2Senders)
				require.Empty(t, cc.jobs)
				require.Empty(t, cc.sent)
				require.Empty(t, b.qObservations)
			}
			b.CloseQBlockTransfers()
			require.Equal(t, bytesBefore, qRetainedBytes.Load())
			require.Equal(t, transfersBefore, qRetainedTransfers.Load())
		})
	}
}

func TestQBlockSupersededNotificationCannotScheduleOldContinuation(t *testing.T) {
	testTransport := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	cc := &blockingQBlockTransport{
		qTestTransport: testTransport,
		firstStarted:   make(chan struct{}),
		releaseFirst:   make(chan struct{}),
	}
	b := New(cc, nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("observe-race"))
	request.SetObserve(0)
	require.NoError(t, b.registerQObservation(request, nil, SZX16))

	makeNotification := func(value byte, mode message.Type, size int) *pool.Message {
		response := pool.NewMessage(context.Background())
		response.SetCode(codes.Content)
		response.SetType(mode)
		response.SetToken(request.Token())
		response.SetObserve(uint32(value))
		response.SetBody(bytes.NewReader(bytes.Repeat([]byte{value}, size)))
		return response
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := b.WriteNotification(makeNotification(1, message.Confirmable, 48), nil)
		firstDone <- err
	}()
	<-cc.firstStarted

	handled, err := b.WriteNotification(makeNotification(2, message.NonConfirmable, 16), nil)
	require.NoError(t, err)
	require.True(t, handled)
	key := q2TransferKey(request, nil)
	close(cc.releaseFirst)
	require.NoError(t, <-firstDone)

	require.Empty(t, cc.jobs, "the stale writer must not schedule an old continuation")
	require.Len(t, cc.sent, 2)
	require.ElementsMatch(t, []message.Type{message.Confirmable, message.NonConfirmable}, []message.Type{cc.sent[0].Type(), cc.sent[1].Type()})
	body, err := b.q2Senders[key].message.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{2}, 16), body)
	require.Empty(t, cc.jobs)
}

func TestQBlockObservationCancellationAndReplacementSchedulingAreOrdered(t *testing.T) {
	testTransport := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	cc := &observeCancelLockTransport{
		qTestTransport: testTransport,
		lockWasFree:    make(chan bool, 1),
	}
	b := New(cc, nil)
	cc.engine = b
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken([]byte("observe-cancel-race"))
	request.SetObserve(0)
	require.NoError(t, b.registerQObservation(request, nil, SZX16))

	notification := func(value byte) *pool.Message {
		response := pool.NewMessage(context.Background())
		response.SetCode(codes.Content)
		response.SetType(message.NonConfirmable)
		response.SetToken(request.Token())
		response.SetObserve(uint32(value))
		response.SetBody(bytes.NewReader(bytes.Repeat([]byte{value}, 48)))
		return response
	}
	handled, err := b.WriteNotification(notification(1), nil)
	require.NoError(t, err)
	require.True(t, handled)
	key := q2TransferKey(request, nil)
	require.Contains(t, cc.jobs, "q2:"+key)

	b.CancelQBlockObservation(request.Token(), nil)
	require.False(t, <-cc.lockWasFree, "job cancellation must run under the sender lock")
	require.NoError(t, b.registerQObservation(request, nil, SZX16))
	handled, err = b.WriteNotification(notification(2), nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, cc.jobs, "q2:"+key, "the replacement continuation must survive old-job cancellation")
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
