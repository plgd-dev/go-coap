package udp_test

import (
	"bytes"
	"context"
	"strings"
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

func TestGoQBlockAutoEndToEnd(t *testing.T) {
	payload := qPayload()
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "udp", true: "dtls"}[secure], func(t *testing.T) {
			calls := new(atomic.Int32)
			counts := new(qWireCounts)
			scheme := "coap://"
			if secure {
				scheme = "coaps://"
			}
			addr := strings.TrimPrefix(startQGoServer(t, secure, blockwise.SZX128, payload, new(qWireCounts), calls), scheme)
			cc := qDial(t, addr, secure, blockwise.SZX128, options.QBlockwiseAuto, counts)

			put := qDo(t, cc, codes.PUT, "/q", payload, message.NonConfirmable)
			require.Equal(t, codes.Changed, put.Code())
			got, err := put.ReadBody()
			require.NoError(t, err)
			require.True(t, bytes.Equal(payload, got))

			get := qDo(t, cc, codes.GET, "/q", nil, message.NonConfirmable)
			require.Equal(t, codes.Content, get.Code())
			got, err = get.ReadBody()
			require.NoError(t, err)
			require.True(t, bytes.Equal(payload, got))

			require.Positive(t, counts.q1.Load(), "Auto upload must use Q-Block1")
			require.Positive(t, counts.q2.Load(), "Auto download must use Q-Block2")
			require.Zero(t, counts.legacy.Load(), "Auto must not silently use RFC 7959")
			require.Equal(t, int32(2), calls.Load())
		})
	}
}

func TestGoQBlockProbeWaitersCancelIndependently(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	started := make(chan struct{}, 1)
	origins := make(chan string, 4)
	var probes, calls atomic.Int32
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/q", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
		calls.Add(1)
		require.NoError(t, w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(bytes.Repeat([]byte{7}, 64))))
	})))
	server := udp.NewServer(options.WithMux(router), options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX16, time.Second),
		options.WithRequestMonitor(func(_ *client.Conn, req *pool.Message) (bool, error) {
			path, _ := req.Path()
			if path == "/.well-known/core" {
				probes.Add(1)
				host, _ := req.GetOptionBytes(message.URIHost)
				origins <- string(host)
				select {
				case started <- struct{}{}:
				default:
				}
				time.Sleep(100 * time.Millisecond)
			}
			return false, nil
		}))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		require.NoError(t, listener.Close())
		require.NoError(t, <-done)
	}()
	cc, err := udp.Dial(listener.LocalAddr().String(), options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX16, time.Second))
	require.NoError(t, err)
	defer cc.Close()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstDone := make(chan error, 1)
	go func() {
		response, requestErr := cc.Get(firstCtx, "/q")
		if response != nil {
			cc.ReleaseMessage(response)
		}
		firstDone <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("capability probe did not start")
	}
	firstCancel()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := cc.Get(ctx, "/q")
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{7}, 64), body)
	require.Equal(t, int32(1), probes.Load())
	require.Equal(t, int32(1), calls.Load())
	for _, host := range []string{"origin-a", "origin-b", "origin-b"} {
		request, err := cc.NewGetRequest(ctx, "/q")
		require.NoError(t, err)
		request.SetOptionString(message.URIHost, host)
		result, requestErr := cc.Do(request)
		cc.ReleaseMessage(request)
		require.NoError(t, requestErr)
		cc.ReleaseMessage(result)
	}
	require.Equal(t, int32(3), probes.Load(), "capabilities must not leak between virtual origins")
	require.Empty(t, <-origins)
	require.Equal(t, "origin-a", <-origins)
	require.Equal(t, "origin-b", <-origins)
}

func TestGoQBlockRepeatedSelectiveRecovery(t *testing.T) {
	payload := qPayload()
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "download", true: "upload"}[upload], func(t *testing.T) {
			option, method := message.QBlock2, codes.GET
			var body []byte
			if upload {
				option, method, body = message.QBlock1, codes.PUT, payload
			}
			calls := new(atomic.Int32)
			server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX128, payload, new(qWireCounts), calls), "coap://")
			innerAddr, inner := newQDropRelay(t, server, option, 3)
			outerAddr, outer := newQDropRelay(t, innerAddr, option, 3)
			cc := qDial(t, outerAddr, false, blockwise.SZX128, options.QBlockwiseOnly, new(qWireCounts))
			response := qDo(t, cc, method, "/q", body, message.NonConfirmable)
			got, err := response.ReadBody()
			require.NoError(t, err)
			require.Equal(t, payload, got)
			require.Equal(t, int32(1), calls.Load())
			observed := inner
			if upload {
				observed = outer
			}
			observed.mu.Lock()
			defer observed.mu.Unlock()
			for num, count := range observed.observed {
				want := 1
				if num == 3 {
					want = 3
				}
				require.Equal(t, want, count, "only the repeatedly lost NUM may be retransmitted: %d", num)
			}
		})
	}
}

func TestGoQBlockNegotiatesSmallerResponse(t *testing.T) {
	payload := qPayload()
	for _, mode := range []message.Type{message.Confirmable, message.NonConfirmable} {
		t.Run(mode.String(), func(t *testing.T) {
			calls := new(atomic.Int32)
			server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX128, payload, new(qWireCounts), calls), "coap://")
			counts := new(qWireCounts)
			cc := qDial(t, server, false, blockwise.SZX1024, options.QBlockwiseOnly, counts)
			for _, method := range []codes.Code{codes.PUT, codes.GET} {
				var body []byte
				if method == codes.PUT {
					body = payload
				}
				response := qDo(t, cc, method, "/q", body, mode)
				got, err := response.ReadBody()
				require.NoError(t, err)
				require.Equal(t, payload, got)
			}
			require.GreaterOrEqual(t, counts.q2.Load(), int32(66))
			require.Equal(t, int32(2), calls.Load())
		})
	}
}
