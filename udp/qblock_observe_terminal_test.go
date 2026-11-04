package udp_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestQBlockObserveTerminalNotificationDuringIncompleteBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	partialReceived := make(chan struct{}, 1)
	var terminalDeliveries atomic.Int32
	router := mux.NewRouter()
	notify := make(chan func(codes.Code, uint32, []byte) error, 2)
	require.NoError(t, router.Handle("/observe-terminal", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		observe, err := r.Observe()
		if err != nil {
			return
		}
		if observe == 1 {
			require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, nil))
			return
		}
		initialBody := bytes.Repeat([]byte{1, 2, 3, 4}, 769)
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(initialBody)))
		w.Message().SetObserve(10)
		cc, token := w.Conn(), bytes.Clone(r.Token())
		notify <- func(code codes.Code, sequence uint32, body []byte) error {
			response := cc.AcquireMessage(ctx)
			defer cc.ReleaseMessage(response)
			response.SetCode(code)
			response.SetToken(token)
			response.SetType(message.NonConfirmable)
			if sequence != 0 {
				response.SetObserve(sequence)
			}
			response.SetContentFormat(message.AppOctets)
			response.SetBody(bytes.NewReader(body))
			return cc.WriteMessage(response)
		}
	})))

	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := udp.NewServer(
		options.WithMux(router),
		options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX128, time.Second),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		require.NoError(t, <-serverDone)
	})

	clientMonitor := options.WithRequestMonitor(func(_ *client.Conn, response *pool.Message) (bool, error) {
		if response.Code() == codes.NotFound {
			return false, nil
		}
		if !response.HasOption(message.QBlock2) {
			return false, nil
		}
		if !response.HasOption(message.Observe) {
			return false, nil
		}
		sequence, sequenceErr := response.Observe()
		if sequenceErr != nil {
			return false, sequenceErr
		}
		if sequence != 11 {
			return false, nil
		}
		value, optionErr := response.GetOptionUint32(message.QBlock2)
		if optionErr != nil {
			return false, optionErr
		}
		_, num, _, decodeErr := blockwise.DecodeQBlockOption(value)
		if decodeErr != nil {
			return false, decodeErr
		}
		if num == 0 {
			return false, nil
		}
		if num == 1 {
			select {
			case partialReceived <- struct{}{}:
			default:
			}
		}
		return true, nil
	})
	cc, dialErr := udp.Dial(listener.LocalAddr().String(),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX128, time.Second),
		clientMonitor,
	)
	require.NoError(t, dialErr)
	defer func() { require.NoError(t, cc.Close()) }()

	type event struct {
		code     codes.Code
		sequence uint32
	}
	events := make(chan event, 8)
	req, requestErr := cc.NewObserveRequest(ctx, "/observe-terminal")
	require.NoError(t, requestErr)
	defer cc.ReleaseMessage(req)
	observationToken := bytes.Clone(req.Token())
	observation, observeErr := cc.DoObserve(req, func(response *pool.Message) {
		sequence, _ := response.Observe()
		if response.Code() == codes.NotFound {
			terminalDeliveries.Add(1)
		}
		events <- event{code: response.Code(), sequence: sequence}
	})
	require.NoError(t, observeErr)

	select {
	case got := <-events:
		require.Equal(t, codes.Content, got.code)
		require.EqualValues(t, 10, got.sequence)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	notifyNext := func() func(codes.Code, uint32, []byte) error {
		t.Helper()
		select {
		case write := <-notify:
			return write
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	writeNotification := notifyNext()
	incompleteBody := bytes.Repeat([]byte{7}, 20*int(blockwise.SZX128.Size()))
	require.NoError(t, writeNotification(codes.Content, 11, incompleteBody))
	select {
	case <-partialReceived:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	require.NoError(t, writeNotification(codes.NotFound, 0, nil))
	select {
	case got := <-events:
		require.Equal(t, codes.NotFound, got.code)
		require.Zero(t, got.sequence, "terminal response has no Observe option")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.EqualValues(t, 1, terminalDeliveries.Load(), "the incomplete notification's terminal result is delivered exactly once")
	require.Eventually(t, observation.Canceled, time.Second, time.Millisecond,
		"terminal delivery must clean up the observation handler")
	require.Never(t, func() bool { return terminalDeliveries.Load() > 1 }, 100*time.Millisecond, time.Millisecond,
		"the terminal notification must not be delivered more than once")
	require.EqualValues(t, 1, terminalDeliveries.Load())

	// Reusing the token proves the terminal delivery also closed the Q route.
	secondReq, secondRequestErr := cc.NewObserveRequest(ctx, "/observe-terminal")
	require.NoError(t, secondRequestErr)
	defer cc.ReleaseMessage(secondReq)
	secondReq.SetToken(observationToken)
	secondObservation, secondObserveErr := cc.DoObserve(secondReq, func(*pool.Message) {})
	require.NoError(t, secondObserveErr)
	require.NoError(t, secondObservation.Cancel(ctx))
}
