package qblock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type failedBodySeek struct {
	reader *bytes.Reader
	err    error
}

func (s failedBodySeek) Read(p []byte) (int, error)     { return s.reader.Read(p) }
func (s failedBodySeek) Seek(int64, int) (int64, error) { return 0, s.err }

func TestReadQBodyStartsAtBeginning(t *testing.T) {
	payload := []byte("complete payload")
	for _, offset := range []int64{0, 3, int64(len(payload))} {
		request := pool.NewMessage(context.Background())
		body := bytes.NewReader(payload)
		_, err := body.Seek(offset, io.SeekStart)
		require.NoError(t, err)
		request.SetBody(body)
		got, err := readQBody(request)
		require.NoError(t, err)
		require.Equal(t, payload, got)
		offset, err = body.Seek(0, io.SeekCurrent)
		require.NoError(t, err)
		require.Zero(t, offset)
	}
	request := pool.NewMessage(context.Background())
	seekErr := errors.New("body seek failed")
	body := bytes.NewReader(payload)
	request.SetBody(failedBodySeek{reader: body, err: seekErr})
	got, err := readQBody(request)
	require.ErrorIs(t, err, seekErr)
	require.Nil(t, got)
	require.Equal(t, len(payload), body.Len())
}

func TestQBlockUploadAfterMonitorReadsBody(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	engine := New(cc, func(err error) { t.Error(err) })
	defer engine.CloseQBlockTransfers()
	payload := []byte("monitored upload")
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.PUT)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte("upload"))
	require.NoError(t, request.SetRequestTag([]byte("tag")))
	request.SetOptionUint32(message.QBlock1, 0)
	request.SetOptionUint32(message.Size1, uint32(len(payload)))
	request.SetBody(bytes.NewReader(payload))
	monitored, err := request.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, monitored)
	writer := responsewriter.New(cc.AcquireMessage(context.Background()), cc)
	defer cc.ReleaseMessage(writer.Message())
	calls := 0
	engine.Handle(writer, request, SZX16, nil,
		func(w *responsewriter.ResponseWriter[*qTestTransport], r *pool.Message) {
			calls++
			got, readErr := r.ReadBody()
			require.NoError(t, readErr)
			require.Equal(t, payload, got)
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
		})
	require.Equal(t, 1, calls)
	require.Equal(t, codes.Changed, writer.Message().Code())
	offset, err := request.Body().Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.Zero(t, offset)
}

func TestQBlock1ResponseObserveWithoutRequestObserveDoesNotFail(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	engine := New(cc, func(err error) { t.Error(err) })
	defer engine.CloseQBlockTransfers()
	payload := []byte("upload")
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.FETCH)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte("no-observe"))
	require.NoError(t, request.SetRequestTag([]byte("tag")))
	value, err := EncodeQBlockOption(SZX16, 0, false)
	require.NoError(t, err)
	request.SetOptionUint32(message.QBlock1, value)
	request.SetOptionUint32(message.Size1, uint32(len(payload)))
	request.SetBody(bytes.NewReader(payload))

	writer := responsewriter.New(cc.AcquireMessage(request.Context()), cc)
	defer cc.ReleaseMessage(writer.Message())
	var handlerCalls int
	err = engine.handleQBlock1(writer, request, SZX16, nil,
		func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
			handlerCalls++
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok"))))
			w.Message().SetObserve(1)
		})
	require.NoError(t, err)
	require.Equal(t, 1, handlerCalls)
	require.Equal(t, codes.Content, writer.Message().Code())
	require.True(t, writer.Message().HasOption(message.Observe))
	require.Empty(t, engine.qObservations, "request without Observe must not create a registration")
}

func TestQBlockObserveReplacementRetainsNewResponseCache(t *testing.T) {
	cc := &qTestTransport{testClient: newTestClient(), jobs: make(map[string]qBlockTestJob)}
	engine := New(cc, func(err error) { t.Error(err) })
	defer engine.CloseQBlockTransfers()
	const token = "reused-observe"
	const tag = "same-tag"
	observation := pool.NewMessage(context.Background())
	observation.SetCode(codes.FETCH)
	observation.SetType(message.NonConfirmable)
	observation.SetToken([]byte(token))
	observation.SetObserve(0)
	require.NoError(t, observation.SetPath("/observe"))
	require.NoError(t, observation.SetRequestTag([]byte(tag)))
	require.NoError(t, engine.registerQObservation(observation, nil, SZX16))

	oldNotification := pool.NewMessage(context.Background())
	oldNotification.SetCode(codes.Content)
	oldNotification.SetType(message.NonConfirmable)
	oldNotification.SetToken([]byte(token))
	oldNotification.SetObserve(1)
	oldNotification.SetBody(bytes.NewReader(bytes.Repeat([]byte{1}, 48)))
	handled, err := engine.WriteNotification(oldNotification, nil)
	require.NoError(t, err)
	require.True(t, handled)

	request := pool.NewMessage(context.Background())
	request.SetCode(codes.FETCH)
	request.SetType(message.NonConfirmable)
	request.SetToken([]byte(token))
	request.SetObserve(0)
	require.NoError(t, request.SetPath("/observe"))
	require.NoError(t, request.SetRequestTag([]byte(tag)))
	qBlock1, err := EncodeQBlockOption(SZX16, 0, false)
	require.NoError(t, err)
	request.SetOptionUint32(message.QBlock1, qBlock1)
	qBlock2, err := EncodeQBlockOption(SZX16, 0, true)
	require.NoError(t, err)
	request.SetOptionUint32(message.QBlock2, qBlock2)
	payload := []byte("upload")
	request.SetOptionUint32(message.Size1, uint32(len(payload)))
	request.SetBody(bytes.NewReader(payload))

	writer := responsewriter.New(cc.AcquireMessage(request.Context()), cc)
	defer cc.ReleaseMessage(writer.Message())
	var handlerCalls int
	err = engine.handleQBlock1(writer, request, SZX16, nil,
		func(w *responsewriter.ResponseWriter[*qTestTransport], _ *pool.Message) {
			handlerCalls++
			require.NoError(t, w.SetResponse(codes.Content, message.AppOctets,
				bytes.NewReader(bytes.Repeat([]byte{2}, 48))))
			w.Message().SetObserve(2)
		})
	require.NoError(t, err)
	require.Equal(t, 1, handlerCalls, "the completed upload should invoke the handler only once")
	key := q2TransferKey(request, nil)
	cached, exists := engine.q2Senders[key]
	require.True(t, exists, "the new response's Q-Block2 cache must survive observation replacement")
	body, err := cached.message.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{2}, 48), body)
}

func TestQBlockTooLargeAdvertisesEffectiveLimit(t *testing.T) {
	for _, limit := range []int64{0, 1024} {
		cc := newTestClient()
		var reported error
		engine := New(cc, func(err error) { reported = err })
		engine.SetMaxReceiveBodySize(limit)
		effective := limit
		if effective == 0 {
			effective = q1DefaultMaxBodySize
		}
		request := pool.NewMessage(context.Background())
		request.SetCode(codes.PUT)
		request.SetType(message.NonConfirmable)
		request.SetToken([]byte("limit"))
		require.NoError(t, request.SetRequestTag([]byte("tag")))
		request.SetOptionUint32(message.QBlock1, 8)
		request.SetOptionUint32(message.Size1, uint32(effective+1))
		request.SetBody(bytes.NewReader(make([]byte, 16)))
		writer := responsewriter.New(cc.AcquireMessage(context.Background()), cc)
		engine.Handle(writer, request, SZX16, nil,
			func(*responsewriter.ResponseWriter[*testClient], *pool.Message) {
				t.Fatal("oversized request reached application")
			})
		require.Equal(t, codes.RequestEntityTooLarge, writer.Message().Code())
		require.ErrorIs(t, reported, ErrQBlockTooLarge)
		size, err := writer.Message().GetOptionUint32(message.Size1)
		require.NoError(t, err)
		require.Equal(t, uint32(effective), size)
		cc.ReleaseMessage(writer.Message())
		engine.CloseQBlockTransfers()
	}
}

type modeTrackingExchange struct {
	qObserveExchange
	modes []message.Type
}

func (e *modeTrackingExchange) Send(req *pool.Message) error {
	e.modes = append(e.modes, req.Type())
	return e.qObserveExchange.Send(req)
}

func TestQBlockReplacementRecoveryUsesNewBodyType(t *testing.T) {
	for _, modes := range [][2]message.Type{
		{message.NonConfirmable, message.Confirmable},
		{message.Confirmable, message.NonConfirmable},
		{message.NonConfirmable, message.Acknowledgement},
	} {
		engine := New(newTestClient(), func(err error) { t.Error(err) })
		req := pool.NewMessage(context.Background())
		req.SetCode(codes.GET)
		req.SetToken([]byte("observe"))
		req.SetType(modes[0])
		req.SetObserve(0)
		exchange := new(modeTrackingExchange)
		receiver := q2BodyReceiver[*testClient]{b: engine, req: req, exchange: exchange, szx: SZX16, maxSZX: SZX16, requestedSet: -1}
		oldSeq, newSeq := uint32(1), uint32(2)
		first := qObservePayload(t, SZX16, 0, 48, 1, &oldSeq)
		first.SetType(modes[0])
		_, err := receiver.accept(first)
		require.NoError(t, err)
		replacement := qObservePayload(t, SZX16, 0, 48, 2, &newSeq)
		replacement.SetType(modes[1])
		_, err = receiver.accept(replacement)
		require.NoError(t, err)
		require.NoError(t, receiver.send([]int64{1}, false))
		want := modes[1]
		if want == message.Acknowledgement {
			want = message.Confirmable
		}
		require.Equal(t, []message.Type{want}, exchange.modes)
		engine.qReleaseBytes(receiver.retainedBytes)
	}
}

func TestQBlockRejectedBodyTypePreservesRecoveryMode(t *testing.T) {
	request := pool.NewMessage(context.Background())
	request.SetType(message.NonConfirmable)
	receiver := q2BodyReceiver[*testClient]{req: request}
	response := pool.NewMessage(context.Background())
	response.SetType(message.NonConfirmable)
	require.NoError(t, receiver.acceptBodyType(response))
	for _, invalid := range []message.Type{message.Confirmable, message.Acknowledgement, message.Reset} {
		response.SetType(invalid)
		require.ErrorIs(t, receiver.acceptBodyType(response), ErrInvalidOptionBlock2)
		require.Equal(t, message.NonConfirmable, request.Type())
	}
}
