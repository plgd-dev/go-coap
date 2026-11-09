package udp_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
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

func TestGoQBlockObserveLifecycle(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, mode := range []options.QBlockwiseMode{options.QBlockwiseAuto, options.QBlockwiseOnly} {
			for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
				t.Run(fmt.Sprintf("secure=%v/mode=%v/type=%v", secure, mode, typ), func(t *testing.T) {
					testQObserveLifecycle(t, secure, mode, typ)
				})
			}
		}
	}
}

func TestQBlockLibcoapObserveGoClient(t *testing.T) {
	if qLibcoapMode(t) != qLibcoapEnabled {
		// The Q-disabled job exercises fallback, not Q notification payloads.
		return
	}
	binary := qLibcoapBinary(t, "coap-server")
	addr := startQLibcoapServer(t, binary, true, blockwise.SZX128)
	for _, secure := range []bool{false, true} {
		for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
			t.Run(fmt.Sprintf("secure=%v/type=%v", secure, typ), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				counts := new(qWireCounts)
				cc := qDial(t, qSecureAddr(t, addr, secure), secure, blockwise.SZX128, options.QBlockwiseOnly, counts)
				original := bytes.Repeat([]byte{1, 2, 3}, 901)
				updated := bytes.Repeat([]byte{4, 5, 6}, 1001)
				response := qDo(t, cc, codes.PUT, "/example_data", original, typ)
				require.Contains(t, []codes.Code{codes.Created, codes.Changed}, response.Code())
				events := make(chan []byte, 8)
				req, err := cc.NewObserveRequest(ctx, "/example_data")
				require.NoError(t, err)
				defer cc.ReleaseMessage(req)
				req.SetType(typ)
				observation, err := cc.DoObserve(req, func(response *pool.Message) {
					body, readErr := response.ReadBody()
					if readErr != nil {
						t.Error(readErr)
						return
					}
					events <- body
				})
				require.NoError(t, err)
				next := func(want []byte) {
					t.Helper()
					select {
					case got := <-events:
						require.Equal(t, want, got)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				next(original)
				before := counts.q2.Load()
				response = qDo(t, cc, codes.PUT, "/example_data", updated, typ)
				require.Contains(t, []codes.Code{codes.Created, codes.Changed}, response.Code())
				next(updated)
				require.Greater(t, counts.q2.Load(), before)
				require.NoError(t, observation.Cancel(ctx))
			})
		}
	}
}

func TestQBlockLibcoapObserveGoServer(t *testing.T) {
	if qLibcoapMode(t) != qLibcoapEnabled {
		return
	}
	binary := qLibcoapBinary(t, "coap-client")
	for _, secure := range []bool{false, true} {
		for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
			t.Run(fmt.Sprintf("secure=%v/type=%v", secure, typ), func(t *testing.T) {
				testQLibcoapObserveGoServer(t, binary, secure, typ)
			})
		}
	}
}

func testQLibcoapObserveGoServer(t *testing.T, binary string, secure bool, typ message.Type) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original := bytes.Repeat([]byte("initial-observe\n"), 101)
	updated := bytes.Repeat([]byte("updated-observe\n"), 111)
	var registrations, cancellations atomic.Int32
	counts := new(qWireCounts)
	writes := make(chan error, 1)
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/observe", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(original)))
		observe, err := r.Observe()
		if err != nil {
			t.Error("continuation reexecuted the observation handler")
			return
		}
		if observe == 1 {
			cancellations.Add(1)
			return
		}
		registrations.Add(1)
		w.Message().SetObserve(10)
		cc, token := w.Conn(), bytes.Clone(r.Token())
		go func() {
			select {
			case <-ctx.Done():
				writes <- ctx.Err()
				return
			case <-time.After(200 * time.Millisecond):
			}
			notification := cc.AcquireMessage(ctx)
			defer cc.ReleaseMessage(notification)
			notification.SetCode(codes.Content)
			notification.SetToken(token)
			notification.SetType(typ)
			notification.SetObserve(11)
			notification.SetBody(bytes.NewReader(updated))
			notification.SetContentFormat(message.AppOctets)
			writes <- cc.WriteMessage(notification)
		}()
	})))
	uri := startQObserveEndpoint(t, secure, router, counts.monitor)
	args := []string{"-L", "7", "-b", "128", "-s", "2", "-B", "8", "-v", "0", "-o", "-"}
	if typ == message.NonConfirmable {
		args = append(args, "-N")
	}
	if secure {
		args = append(args, "-u", "qblock-test", "-k", qLibcoapPSK)
	}
	args = append(args, uri+"/observe")
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), string(original))
	require.Contains(t, string(output), string(updated))
	select {
	case err := <-writes:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, int32(1), registrations.Load())
	require.Equal(t, int32(1), cancellations.Load())
	require.Positive(t, counts.q2.Load(), "native Observe registration must actually use Q")
	require.Zero(t, counts.legacy.Load())
}

func startQObserveEndpoint(t *testing.T, secure bool, router *mux.Router, monitor func(*client.Conn, *pool.Message) (bool, error)) string {
	t.Helper()
	done := make(chan error, 1)
	if secure {
		listener, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", coapNet.NewDTLSServerOptions(
			piondtls.WithPSK(qLibcoapPSKCallback), piondtls.WithPSKIdentityHint([]byte("qblock-test")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256)))
		require.NoError(t, err)
		server := dtls.NewServer(options.WithMux(router), options.WithQBlockwise(options.QBlockwiseAuto),
			options.WithBlockwise(true, blockwise.SZX128, time.Second), options.WithRequestMonitor(monitor))
		go func() { done <- server.Serve(listener) }()
		t.Cleanup(func() { server.Stop(); require.NoError(t, listener.Close()); require.NoError(t, <-done) })
		return "coaps://" + listener.Addr().String()
	}
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := udp.NewServer(options.WithMux(router), options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX128, time.Second),
		options.WithRequestMonitor(monitor))
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); require.NoError(t, listener.Close()); require.NoError(t, <-done) })
	return "coap://" + listener.LocalAddr().String()
}

func testQObserveLifecycle(t *testing.T, secure bool, mode options.QBlockwiseMode, typ message.Type) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte{1, 2, 3, 4}, 769)
	notify := make(chan func(uint32, []byte) error, 1)
	var registrations, cancellations atomic.Int32
	state := qObserveLifecycleMonitor{partial: make(chan struct{}, 1)}
	state.observationToken.Store("")
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/observe", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		observe, err := r.Observe()
		if err != nil {
			t.Error("recovery reexecuted application")
			return
		}
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(payload)))
		if observe == 1 {
			cancellations.Add(1)
			return
		}
		registrations.Add(1)
		w.Message().SetObserve(10)
		token := bytes.Clone(r.Token())
		cc := w.Conn()
		notify <- func(sequence uint32, body []byte) error {
			response := cc.AcquireMessage(ctx)
			defer cc.ReleaseMessage(response)
			response.SetCode(codes.Content)
			response.SetToken(token)
			response.SetType(typ)
			response.SetObserve(sequence)
			response.SetContentFormat(message.AppOctets)
			response.SetBody(bytes.NewReader(body))
			return cc.WriteMessage(response)
		}
	})))
	uri := startQObserveEndpoint(t, secure, router, state.request)
	addr := strings.TrimPrefix(strings.TrimPrefix(uri, "coaps://"), "coap://")
	clientMonitor := options.WithRequestMonitor(state.response)
	cc := qDialWithOptions(t, addr, secure, options.WithQBlockwise(mode),
		options.WithBlockwise(true, blockwise.SZX128, time.Second), clientMonitor)
	defer func() { require.NoError(t, cc.Close()) }()
	type event struct {
		sequence uint32
		body     []byte
	}
	events := make(chan event, 8)
	req, err := cc.NewObserveRequest(ctx, "/observe")
	require.NoError(t, err)
	defer cc.ReleaseMessage(req)
	req.SetType(typ)
	state.observationToken.Store(string(req.Token()))
	observation, err := cc.DoObserve(req, func(response *pool.Message) {
		body, readErr := response.ReadBody()
		if readErr != nil {
			t.Error(readErr)
		}
		sequence, _ := response.Observe()
		events <- event{sequence, body}
	})
	require.NoError(t, err)
	readEvent := func(sequence uint32, want []byte) {
		t.Helper()
		select {
		case got := <-events:
			require.Equal(t, sequence, got.sequence)
			require.Equal(t, want, got.body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	readEvent(10, payload)
	write := <-notify
	updated := bytes.Repeat([]byte{5, 6, 7}, 1101)
	require.NoError(t, write(11, updated))
	readEvent(11, updated)
	require.True(t, state.dropped.Load())
	if typ == message.NonConfirmable {
		require.Positive(t, state.recoveries.Load())
		require.NoError(t, write(12, bytes.Repeat([]byte{9}, 1<<20)))
		select {
		case <-state.partial:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		replacement := bytes.Repeat([]byte{7, 8, 9}, 1201)
		require.NoError(t, write(13, replacement))
		readEvent(13, replacement)
		require.NoError(t, write(14, bytes.Repeat([]byte{8}, 1<<20)))
		select {
		case <-state.partial:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	state.canceling.Store(true)
	require.NoError(t, observation.Cancel(ctx))
	require.True(t, observation.Canceled())
	require.Equal(t, int32(1), registrations.Load())
	require.Equal(t, int32(1), cancellations.Load())
	require.Positive(t, state.qPayloads.Load())
	require.Zero(t, state.invalidObserve.Load())
}

func TestQBlockFETCHObserveLifecycle(t *testing.T) {
	for _, initialSize := range []int{32, 2103} {
		t.Run(fmt.Sprintf("initial-response-size=%d", initialSize), func(t *testing.T) {
			testQBlockFETCHObserveLifecycle(t, initialSize)
		})
	}
}

func testQBlockFETCHObserveLifecycle(t *testing.T, initialSize int) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	requestBody := bytes.Repeat([]byte{1, 2, 3}, 901)
	initialBody := bytes.Repeat([]byte{4}, initialSize)
	updatedBody := bytes.Repeat([]byte{7, 8, 9}, 801)
	var registrations, cancellations atomic.Int32
	notifications := make(chan func() error, 1)
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/fetch-observe", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		observe, err := r.Observe()
		if err != nil {
			t.Errorf("cannot read Observe: %v", err)
			return
		}
		switch {
		case r.Code() == codes.FETCH && observe == 0:
			body, readErr := r.ReadBody()
			if readErr != nil {
				t.Errorf("cannot read FETCH body: %v", readErr)
				return
			}
			if !bytes.Equal(body, requestBody) {
				t.Errorf("FETCH body mismatch: got %d bytes, want %d", len(body), len(requestBody))
				return
			}
			registrations.Add(1)
			require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(initialBody)))
			w.Message().SetObserve(10)
			token := bytes.Clone(r.Token())
			serverConn := w.Conn()
			notifications <- func() error {
				response := serverConn.AcquireMessage(ctx)
				defer serverConn.ReleaseMessage(response)
				response.SetCode(codes.Content)
				response.SetToken(token)
				response.SetObserve(11)
				response.SetContentFormat(message.AppOctets)
				response.SetBody(bytes.NewReader(updatedBody))
				return serverConn.WriteMessage(response)
			}
		case observe == 1:
			cancellations.Add(1)
			require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(nil)))
		default:
			t.Errorf("unexpected request method/Observe: %v/%v", r.Code(), observe)
		}
	})))
	uri := startQObserveEndpoint(t, false, router, func(*client.Conn, *pool.Message) (bool, error) {
		return false, nil
	})
	addr := strings.TrimPrefix(uri, "coap://")
	cc := qDialWithOptions(t, addr, false, options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX128, time.Second))
	defer func() { require.NoError(t, cc.Close()) }()

	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.FETCH)
	req.SetType(message.Confirmable)
	req.SetToken([]byte("fetchobs"))
	require.NoError(t, req.SetPath("/fetch-observe"))
	req.SetObserve(0)
	req.SetContentFormat(message.AppOctets)
	req.SetBody(bytes.NewReader(requestBody))
	type event struct {
		sequence uint32
		body     []byte
	}
	events := make(chan event, 4)
	observation, err := cc.DoObserve(req, func(response *pool.Message) {
		body, readErr := response.ReadBody()
		if readErr != nil {
			t.Errorf("cannot read Observe body: %v", readErr)
			return
		}
		sequence, _ := response.Observe()
		events <- event{sequence: sequence, body: body}
	})
	require.NoError(t, err)
	assertEvent := func(sequence uint32, expected []byte) {
		t.Helper()
		select {
		case got := <-events:
			require.Equal(t, sequence, got.sequence)
			require.Equal(t, expected, got.body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assertEvent(10, initialBody)
	select {
	case notify := <-notifications:
		require.NoError(t, notify())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertEvent(11, updatedBody)
	require.NoError(t, observation.Cancel(ctx))
	require.Equal(t, int32(1), registrations.Load())
	require.Equal(t, int32(1), cancellations.Load())
}

type qObserveLifecycleMonitor struct {
	recoveries, qPayloads, invalidObserve atomic.Int32
	dropped, canceling                    atomic.Bool
	observationToken                      atomic.Value
	partial                               chan struct{}
}

func (s *qObserveLifecycleMonitor) request(_ *client.Conn, req *pool.Message) (bool, error) {
	if req.HasOption(message.QBlock2) {
		value, _ := req.GetOptionUint32(message.QBlock2)
		_, num, more, _ := blockwise.DecodeQBlockOption(value)
		if num != 0 && !more {
			s.recoveries.Add(1)
			if req.HasOption(message.Observe) {
				s.invalidObserve.Add(1)
			}
		}
	}
	return false, nil
}

func (s *qObserveLifecycleMonitor) response(_ *client.Conn, response *pool.Message) (bool, error) {
	if !response.HasOption(message.QBlock2) {
		return false, nil
	}
	value, optionErr := response.GetOptionUint32(message.QBlock2)
	if optionErr != nil {
		return false, optionErr
	}
	s.qPayloads.Add(1)
	originalToken, _ := s.observationToken.Load().(string)
	if !s.canceling.Load() && string(response.Token()) == originalToken && !response.HasOption(message.Observe) {
		s.invalidObserve.Add(1)
	}
	_, num, _, decodeErr := blockwise.DecodeQBlockOption(value)
	if seq, err := response.Observe(); decodeErr == nil && err == nil && seq == 11 && num == 3 && s.dropped.CompareAndSwap(false, true) {
		return true, nil
	}
	if seq, err := response.Observe(); err == nil && (seq == 12 || seq == 14) {
		if num != 0 {
			return true, nil
		}
		select {
		case s.partial <- struct{}{}:
		default:
		}
	}
	return false, nil
}
