package udp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/noresponse"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/oscore/proxy"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestOSCOREAndPlaintextUDPExchange(t *testing.T) {
	listener, listenerErr := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	serverProcessor := newUDPOSCOREProcessor(t, "server", "client", true)
	server := udp.NewServer(
		options.WithOSCORE(serverProcessor),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			if req.Code() != codes.GET || req.HasOption(message.OSCORE) {
				if err := w.SetResponse(codes.BadRequest, message.TextPlain, nil); err != nil {
					t.Errorf("set bad-request response: %v", err)
				}
				return
			}
			path, pathErr := req.Path()
			if pathErr != nil {
				t.Errorf("get request path: %v", pathErr)
				return
			}
			if path == "/error" {
				if err := w.SetResponse(codes.BadRequest, message.TextPlain, nil); err != nil {
					t.Errorf("set application-error response: %v", err)
				}
				return
			}
			if path == "/unauthorized" {
				if err := w.SetResponse(codes.Unauthorized, message.TextPlain, nil); err != nil {
					t.Errorf("set application-error response: %v", err)
				}
				return
			}
			if path == "/server-error" {
				if err := w.SetResponse(codes.InternalServerError, message.TextPlain, nil); err != nil {
					t.Errorf("set application-error response: %v", err)
				}
				return
			}
			if err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok"))); err != nil {
				t.Errorf("set content response: %v", err)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP server did not stop")
		}
	}()

	secureProcessor := newUDPOSCOREProcessor(t, "client", "server", true)
	secureClient, err := udp.Dial(listener.LocalAddr().String(), options.WithOSCORE(secureProcessor))
	require.NoError(t, err)
	defer func() { require.NoError(t, secureClient.Close()) }()
	plaintextClient, err := udp.Dial(listener.LocalAddr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()

	assertUDPGetResponse(t, secureClient)
	assertUDPGetResponse(t, plaintextClient)

	for _, test := range []struct {
		path string
		code codes.Code
	}{
		{path: "/error", code: codes.BadRequest},
		{path: "/unauthorized", code: codes.Unauthorized},
		{path: "/server-error", code: codes.InternalServerError},
	} {
		t.Run(test.path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request, err := secureClient.NewGetRequest(ctx, test.path)
			require.NoError(t, err)
			defer secureClient.ReleaseMessage(request)
			response, err := secureClient.Do(request)
			require.NoError(t, err)
			defer secureClient.ReleaseMessage(response)
			require.Equal(t, test.code, response.Code())
			require.False(t, oscore.IsUnprotectedProcessingError(response.Context()),
				"application errors to protected requests must be protected, not treated as OSCORE processing errors")
		})
	}
}

func TestOSCOREOuterBlockwiseStrictRejectsPlaintextRequests(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	serverErrors := make(chan error, 8)
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithOSCOREOuterBlockwise(),
		options.WithErrors(func(err error) {
			select {
			case serverErrors <- err:
			default:
			}
		}),
		options.WithHandlerFunc(func(_ *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "strict OSCORE Outer Blockwise server did not stop")
		}
	}()

	rawClient, err := udp.Dial(listener.LocalAddr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, rawClient.Close()) }()
	for i, test := range []struct {
		name     string
		code     codes.Code
		optionID message.OptionID
	}{
		{name: "Block1", code: codes.POST, optionID: message.Block1},
		{name: "Block2", code: codes.GET, optionID: message.Block2},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := rawClient.AcquireMessage(context.Background())
			defer rawClient.ReleaseMessage(request)
			request.SetCode(test.code)
			request.SetType(message.NonConfirmable)
			request.SetToken(message.Token{byte(i + 1)})
			block, encodeErr := blockwise.EncodeBlockOption(blockwise.SZX1024, 0, false)
			require.NoError(t, encodeErr)
			request.SetOptionUint32(test.optionID, block)
			if test.optionID == message.Block1 {
				request.SetBody(bytes.NewReader([]byte("plaintext")))
			}
			require.NoError(t, rawClient.WriteMessage(request))

			select {
			case serverErr := <-serverErrors:
				require.ErrorIs(t, serverErr, oscore.ErrPlaintextDisabled)
			case <-time.After(2 * time.Second):
				require.Fail(t, "strict OSCORE policy did not reject plaintext "+test.name)
			}
			require.Zero(t, handled.Load(), "plaintext request reached the application")
		})
	}
}

func TestOSCOREOuterBlockwiseUDPExchange(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("outer-blockwise-"), 512)
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithOSCOREOuterBlockwise(),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			if req.HasOption(message.OSCORE) || req.HasOption(message.Block1) || req.HasOption(message.Block2) {
				t.Errorf("handler received outer OSCORE or Blockwise options: %v", req.Options())
				return
			}
			body, readErr := req.ReadBody()
			if readErr != nil {
				t.Errorf("read reassembled OSCORE request body: %v", readErr)
				return
			}
			if !bytes.Equal(body, payload) {
				t.Errorf("request body length %d, want %d", len(body), len(payload))
				return
			}
			if responseErr := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(payload)); responseErr != nil {
				t.Errorf("set large OSCORE response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP Outer Blockwise server did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(newUDPOSCOREProcessor(t, "client", "server", false)),
		options.WithOSCOREOuterBlockwise(),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := cc.NewPostRequest(ctx, "/outer-blockwise", message.AppOctets, bytes.NewReader(payload))
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.Content, response.Code())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.False(t, response.HasOption(message.OSCORE))
	require.False(t, response.HasOption(message.Block2))
}

func TestOSCOREOuterBlockwiseRejectsOversizedProtectedResponse(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	const maxPayloadSize = 4096
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithOSCOREOuterBlockwise(),
		options.WithOSCOREOuterBlockwiseMaxReassembledPayloadSize(maxPayloadSize),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			if responseErr := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(make([]byte, maxPayloadSize+1))); responseErr != nil {
				t.Errorf("set oversized OSCORE response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP Outer Blockwise server did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(newUDPOSCOREProcessor(t, "client", "server", false)),
		options.WithOSCOREOuterBlockwise(),
		options.WithOSCOREOuterBlockwiseMaxReassembledPayloadSize(maxPayloadSize),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := cc.NewGetRequest(ctx, "/oversized-response")
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.RequestEntityTooLarge, response.Code())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Empty(t, body)
	require.False(t, response.HasOption(message.OSCORE))
}

func TestOSCOREOuterBlockwiseClientRejectsPlaintextBlock2Responses(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("plaintext-block2-"), 128)
	var handled atomic.Int32
	server := udp.NewServer(
		options.WithOSCOREProxy(),
		options.WithOSCOREOuterBlockwise(),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			requestNumber := handled.Add(1)
			code := codes.Content
			if requestNumber == 2 {
				code = codes.InternalServerError
			}
			if responseErr := w.SetResponse(code, message.AppOctets, bytes.NewReader(payload)); responseErr != nil {
				t.Errorf("set plaintext Block2 response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "plaintext-response server did not stop")
		}
	}()

	clientErrors := make(chan error, 8)
	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(newUDPOSCOREProcessor(t, "client", "server", false)),
		options.WithOSCOREOuterBlockwise(),
		options.WithErrors(func(err error) {
			select {
			case clientErrors <- err:
			default:
			}
		}),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "success", path: "/success"},
		{name: "application error", path: "/application-error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			request, requestErr := cc.NewGetRequest(ctx, test.path)
			require.NoError(t, requestErr)
			defer cc.ReleaseMessage(request)

			response, doErr := cc.Do(request)
			if response != nil {
				cc.ReleaseMessage(response)
			}
			require.ErrorIs(t, doErr, context.DeadlineExceeded,
				"plaintext Block2 response must not complete the protected request")
			select {
			case clientErr := <-clientErrors:
				require.ErrorIs(t, clientErr, oscore.ErrOSCORERequired)
			case <-time.After(time.Second):
				require.Fail(t, "strict OSCORE client did not report the plaintext response")
			}
		})
	}
	require.Equal(t, int32(2), handled.Load(), "both requests should reach the test server")
}

type outerBlockwiseProxyRoundTripper struct {
	context         *oscore.Context
	expectedPayload []byte
	called          bool
}

func (r *outerBlockwiseProxyRoundTripper) RoundTrip(
	_ context.Context, target oscore.ProxyTarget, request message.Message,
) (message.Message, error) {
	r.called = true
	if target.Host != "origin.example" {
		return message.Message{}, fmt.Errorf("unexpected origin target %q", target.Host)
	}
	plaintext, exchange, err := r.context.UnprotectRequest(request)
	if err != nil {
		return message.Message{}, err
	}
	if !bytes.Equal(plaintext.Payload, r.expectedPayload) {
		return message.Message{}, fmt.Errorf("origin received %d bytes, want %d", len(plaintext.Payload), len(r.expectedPayload))
	}
	return r.context.ProtectResponse(message.Message{
		Code:    codes.Content,
		Token:   append(message.Token(nil), request.Token...),
		Payload: append([]byte(nil), r.expectedPayload...),
	}, exchange)
}

func TestOSCOREOuterBlockwiseUDPProxy(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	clientContext, originContext := newUDPOSCOREContextPair(t, "client", "origin")
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	payload := bytes.Repeat([]byte("proxied-outer-blockwise-"), 384)
	origin := &outerBlockwiseProxyRoundTripper{context: originContext, expectedPayload: payload}
	forwarder := &proxy.Forwarder{
		RoundTripper: origin,
		AllowTarget: func(target oscore.ProxyTarget) bool {
			return target.Host == "origin.example"
		},
	}
	server := udp.NewServer(
		options.WithOSCOREProxy(),
		options.WithOSCOREOuterBlockwise(),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			requestBody, readErr := req.ReadBody()
			if readErr != nil {
				t.Errorf("read reassembled proxy request: %v", readErr)
				return
			}
			response, forwardErr := forwarder.Forward(req.Context(), message.Message{
				Token:   append(message.Token(nil), req.Token()...),
				Code:    req.Code(),
				Type:    req.Type(),
				Options: req.Options(),
				Payload: requestBody,
			})
			if forwardErr != nil {
				t.Errorf("forward reassembled OSCORE request: %v", forwardErr)
				return
			}
			w.Message().SetCode(response.Code)
			w.Message().ResetOptionsTo(response.Options)
			w.Message().SetBody(bytes.NewReader(response.Payload))
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP OSCORE proxy did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(processor),
		options.WithOSCOREOuterBlockwise(),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.POST)
	request.SetOptionBytes(message.ProxyURI, []byte("coap://origin.example/resource"))
	request.SetBody(bytes.NewReader(payload))
	response, err := cc.Do(request)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.True(t, origin.called, "proxy did not forward the reassembled request")
	require.Equal(t, codes.Content, response.Code())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.False(t, response.HasOption(message.OSCORE))
	require.False(t, response.HasOption(message.Block2))
}

func TestOSCOREUDPObserveWithPOSTIsRejectedBeforeDispatch(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	serverErrors := make(chan error, 1)
	var handled atomic.Int32
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithErrors(func(err error) { serverErrors <- err }),
		options.WithHandlerFunc(func(_ *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP Observe validation server did not stop")
		}
	}()

	rawClient, err := udp.Dial(listener.LocalAddr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, rawClient.Close()) }()
	senderContext, _ := newUDPOSCOREContextPair(t, "client", "server")
	innerOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	inner, err := oscore.EncodeInner(codes.POST, innerOptions, nil)
	require.NoError(t, err)
	partialIV, ciphertext, err := senderContext.Seal(inner, func(partialIV []byte) ([]byte, error) {
		return oscore.BuildAADWithAlgorithm(senderContext.Algorithm(), senderContext.SenderID(), partialIV, nil)
	})
	require.NoError(t, err)
	oscoreOption, err := (oscore.OptionValue{
		PartialIV:   partialIV,
		SenderID:    []byte("client"),
		SenderIDSet: true,
	}).MarshalBinary()
	require.NoError(t, err)
	outerOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request := rawClient.AcquireMessage(context.Background())
	request.SetMessage(message.Message{
		Code:  codes.FETCH,
		Type:  message.NonConfirmable,
		Token: message.Token{0x71},
		Options: append(outerOptions, message.Option{
			ID:    message.OSCORE,
			Value: oscoreOption,
		}),
		Payload: ciphertext,
	})
	require.NoError(t, rawClient.WriteMessage(request))
	rawClient.ReleaseMessage(request)

	select {
	case serverErr := <-serverErrors:
		require.ErrorIs(t, serverErr, oscore.ErrUnsupportedOption)
	case <-time.After(time.Second):
		t.Fatal("server did not reject Observe on POST")
	}
	require.Zero(t, handled.Load(), "invalid Observe+POST request reached the application")
}

func TestOSCOREUDPRejectsMalformedTransportMessages(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	serverErrors := make(chan error, 4)
	handled := make(chan struct{}, 1)
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithErrors(func(err error) {
			serverErrors <- err
		}),
		options.WithHandlerFunc(func(_ *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled <- struct{}{}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP malformed-message server did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()
	validOSCOREOption, err := (oscore.OptionValue{
		PartialIV: []byte{1}, SenderID: []byte("client"), SenderIDSet: true,
	}).MarshalBinary()
	require.NoError(t, err)

	malformedMessages := []struct {
		name    string
		code    codes.Code
		options message.Options
	}{
		{
			name:    "truncated OSCORE option",
			code:    codes.POST,
			options: message.Options{{ID: message.OSCORE, Value: []byte{0x09}}},
		},
		{
			name: "duplicate OSCORE options",
			code: codes.POST,
			options: message.Options{
				{ID: message.OSCORE, Value: validOSCOREOption},
				{ID: message.OSCORE, Value: validOSCOREOption},
			},
		},
	}
	for i, test := range malformedMessages {
		t.Run(test.name, func(t *testing.T) {
			request := cc.AcquireMessage(context.Background())
			request.SetMessage(message.Message{
				Code:      test.code,
				Type:      message.NonConfirmable,
				MessageID: int32(i + 1),
				Token:     message.Token{byte(i + 1)},
				Options:   test.options,
			})
			require.NoError(t, cc.WriteMessage(request))
			cc.ReleaseMessage(request)

			select {
			case serverErr := <-serverErrors:
				require.ErrorIs(t, serverErr, oscore.ErrInvalidOSCOREMessage)
			case <-time.After(time.Second):
				t.Fatal("server did not report malformed OSCORE request")
			}
		})
	}
	select {
	case <-handled:
		t.Fatal("malformed OSCORE request reached application handler")
	default:
	}
}

func TestOSCOREUDPNoResponse(t *testing.T) {
	listener, listenerErr := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	var handled atomic.Int32
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			handled.Add(1)
			err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok")))
			if req.HasOption(message.NoResponse) {
				if !errors.Is(err, noresponse.ErrMessageNotInterested) {
					t.Errorf("expected No-Response suppression, got %v", err)
				}
			} else if err != nil {
				t.Errorf("set response: %v", err)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP server did not stop")
		}
	}()

	processor := newUDPOSCOREProcessor(t, "client", "server", false)
	cc, err := udp.Dial(listener.LocalAddr().String(), options.WithOSCORE(processor))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()
	plaintextClient, err := udp.Dial(listener.LocalAddr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()

	sendNoResponseRequest := func(requestClient *client.Conn) error {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		request, err := requestClient.NewGetRequest(ctx, "/oscore-noresponse")
		if err != nil {
			return err
		}
		request.SetOptionUint32(message.NoResponse, 2)
		defer requestClient.ReleaseMessage(request)
		_, err = requestClient.Do(request)
		return err
	}
	require.ErrorIs(t, sendNoResponseRequest(plaintextClient), context.DeadlineExceeded)
	require.EqualValues(t, 0, handled.Load(), "strict OSCORE policy dispatched plaintext")

	require.ErrorIs(t, sendNoResponseRequest(cc), context.DeadlineExceeded)
	require.EqualValues(t, 1, handled.Load())

	assertUDPGetResponse(t, cc)
	require.EqualValues(t, 2, handled.Load())
}

func TestOSCOREUDPMultipleOutgoingContexts(t *testing.T) {
	listener, listenerErr := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	clientA, serverA := newUDPOSCOREContextPair(t, "ca", "sa")
	clientB, serverB := newUDPOSCOREContextPair(t, "cb", "sb")

	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts: []*oscore.Context{serverA, serverB},
	})
	require.NoError(t, err)
	server := udp.NewServer(
		options.WithOSCORE(serverProcessor),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			path, pathErr := req.Options().GetString(message.URIPath)
			if pathErr != nil {
				t.Errorf("get request path: %v", pathErr)
				return
			}
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(path))); responseErr != nil {
				t.Errorf("set content response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP multi-context server did not stop")
		}
	}()

	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts: []*oscore.Context{clientA, clientB},
		OutgoingContextSelector: func(request message.Message, _ []*oscore.Context) (*oscore.Context, error) {
			path, pathErr := request.Options.GetString(message.URIPath)
			if pathErr != nil {
				return nil, pathErr
			}
			switch path {
			case "peer-a":
				return clientA, nil
			case "peer-b":
				return clientB, nil
			default:
				return nil, oscore.ErrNoSecurityContext
			}
		},
	})
	require.NoError(t, err)
	cc, err := udp.Dial(listener.LocalAddr().String(), options.WithOSCORE(processor))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	for _, path := range []string{"peer-a", "peer-b"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		request, err := cc.NewGetRequest(ctx, "/"+path)
		require.NoError(t, err)
		response, err := cc.Do(request)
		cancel()
		require.NoError(t, err)
		require.Equal(t, codes.Content, response.Code())
		body, err := response.ReadBody()
		require.NoError(t, err)
		require.Equal(t, []byte(path), body)
		cc.ReleaseMessage(request)
		cc.ReleaseMessage(response)
	}
}

func TestOSCOREUDPObserveNotificationsAndCancellation(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	clientContext, serverContext := newUDPOSCOREContextPair(t, "client", "server")
	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{serverContext}})
	require.NoError(t, err)
	sendNotifications := make(chan struct{})
	server := udp.NewServer(
		options.WithOSCORE(serverProcessor),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			observe, observeErr := req.Observe()
			if observeErr != nil {
				t.Errorf("get Observe request option: %v", observeErr)
				return
			}
			switch observe {
			case 0:
				if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("initial"))); responseErr != nil {
					t.Errorf("set initial response: %v", responseErr)
					return
				}
				w.Message().SetObserve(0)
				cc, token := w.Conn(), append(message.Token(nil), req.Token()...)
				go func() {
					<-sendNotifications
					for sequence := uint32(1); sequence <= 2; sequence++ {
						notification := cc.AcquireMessage(cc.Context())
						notification.SetCode(codes.Content)
						notification.SetToken(token)
						notification.SetObserve(sequence)
						notification.SetBody(bytes.NewReader([]byte(fmt.Sprintf("notification-%d", sequence))))
						writeErr := cc.WriteMessage(notification)
						cc.ReleaseMessage(notification)
						if writeErr != nil {
							t.Errorf("write Observe notification: %v", writeErr)
							return
						}
					}
				}()
			case 1:
				if responseErr := w.SetResponse(codes.Content, message.TextPlain, nil); responseErr != nil {
					t.Errorf("set cancellation response: %v", responseErr)
				}
			default:
				t.Errorf("unexpected Observe value %d", observe)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP Observe server did not stop")
		}
	}()

	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	cc, err := udp.Dial(listener.LocalAddr().String(), options.WithOSCORE(processor))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	received := make(chan string, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observation, err := cc.Observe(ctx, "/oscore-observe", func(response *pool.Message) {
		body, readErr := response.ReadBody()
		if readErr != nil {
			t.Errorf("read Observe response: %v", readErr)
			return
		}
		if response.HasOption(message.OSCORE) {
			t.Errorf("Observe callback received an OSCORE envelope")
		}
		received <- string(body)
	})
	require.NoError(t, err)
	require.Equal(t, "initial", <-received)
	close(sendNotifications)
	require.Equal(t, "notification-1", <-received)
	require.Equal(t, "notification-2", <-received)
	require.NoError(t, observation.Cancel(ctx))
}

func TestOSCOREUDPClassicBlockwise(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	requestBody := bytes.Repeat([]byte("request-block-"), 500)
	responseBody := bytes.Repeat([]byte("response-block-"), 500)
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithBlockwise(true, blockwise.SZX256, 5*time.Second),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			got, readErr := req.ReadBody()
			if readErr != nil {
				t.Errorf("read assembled request: %v", readErr)
				return
			}
			if !bytes.Equal(requestBody, got) {
				t.Errorf("request body length %d, want %d", len(got), len(requestBody))
			}
			if responseErr := w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(responseBody)); responseErr != nil {
				t.Errorf("set response body: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP blockwise server did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(newUDPOSCOREProcessor(t, "client", "server", false)),
		options.WithBlockwise(true, blockwise.SZX256, 5*time.Second),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := cc.NewPutRequest(ctx, "/oscore-blockwise", message.AppOctets, bytes.NewReader(requestBody))
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.Changed, response.Code())
	got, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, responseBody, got)
}

func TestOSCOREUDPQBlock(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	requestBody := bytes.Repeat([]byte("qblock-oscore-request-"), 240)
	responseBody := bytes.Repeat([]byte("qblock-oscore-response-"), 240)
	var handledUpload atomic.Bool
	server := udp.NewServer(
		options.WithOSCORE(newUDPOSCOREProcessor(t, "server", "client", false)),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			switch req.Code() {
			case codes.PUT:
				got, readErr := req.ReadBody()
				if readErr != nil {
					t.Errorf("read assembled Q-Block OSCORE upload: %v", readErr)
					return
				}
				if !bytes.Equal(requestBody, got) {
					t.Errorf("Q-Block OSCORE upload length %d, want %d", len(got), len(requestBody))
				}
				handledUpload.Store(true)
				if responseErr := w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("uploaded"))); responseErr != nil {
					t.Errorf("set upload response: %v", responseErr)
				}
			case codes.GET:
				if responseErr := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(responseBody)); responseErr != nil {
					t.Errorf("set Q-Block OSCORE download response: %v", responseErr)
				}
			default:
				t.Errorf("unexpected request code %v", req.Code())
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP Q-Block OSCORE server did not stop")
		}
	}()

	cc, err := udp.Dial(listener.LocalAddr().String(),
		options.WithOSCORE(newUDPOSCOREProcessor(t, "client", "server", false)),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	putRequest, err := cc.NewPutRequest(ctx, "/qblock", message.AppOctets, bytes.NewReader(requestBody))
	require.NoError(t, err)
	putResponse, err := cc.Do(putRequest)
	cc.ReleaseMessage(putRequest)
	require.NoError(t, err)
	require.Equal(t, codes.Changed, putResponse.Code())
	uploadResponseBody, err := putResponse.ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("uploaded"), uploadResponseBody)
	cc.ReleaseMessage(putResponse)
	require.True(t, handledUpload.Load())

	getRequest, err := cc.NewGetRequest(ctx, "/qblock")
	require.NoError(t, err)
	getResponse, err := cc.Do(getRequest)
	cc.ReleaseMessage(getRequest)
	require.NoError(t, err)
	require.Equal(t, codes.Content, getResponse.Code())
	got, err := getResponse.ReadBody()
	require.NoError(t, err)
	require.Equal(t, responseBody, got)
	cc.ReleaseMessage(getResponse)
}

func assertUDPGetResponse(t *testing.T, cc *client.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := cc.NewGetRequest(ctx, "/oscore")
	require.NoError(t, err)
	defer cc.ReleaseMessage(request)
	response, err := cc.Do(request)
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.Content, response.Code())
	require.False(t, response.HasOption(message.OSCORE))
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), body)
}

func newUDPOSCOREProcessor(t *testing.T, senderID, recipientID string, allowPlaintext bool) *oscore.Processor {
	t.Helper()
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SenderID:     []byte(senderID),
		RecipientID:  []byte(recipientID),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:         []*oscore.Context{securityContext},
		AllowUnprotected: allowPlaintext,
	})
	require.NoError(t, err)
	return processor
}

func newUDPOSCOREContextPair(t *testing.T, clientID, serverID string) (*oscore.Context, *oscore.Context) {
	t.Helper()
	masterSecret := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte(clientID),
		RecipientID:  []byte(serverID),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte(serverID),
		RecipientID:  []byte(clientID),
	})
	require.NoError(t, err)
	return clientContext, serverContext
}
