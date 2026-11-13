package tcp_test

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
	observationNet "github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	tcpserver "github.com/plgd-dev/go-coap/v3/tcp/server"
	"github.com/stretchr/testify/require"
)

func TestOSCOREAndPlaintextTCPExchange(t *testing.T) {
	listener, listenerErr := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", true)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			if req.Code() != codes.GET || req.HasOption(message.OSCORE) {
				if err := w.SetResponse(codes.BadRequest, message.TextPlain, nil); err != nil {
					t.Errorf("set bad-request response: %v", err)
				}
				return
			}
			path, err := req.Path()
			if err != nil {
				t.Errorf("get request path: %v", err)
				return
			}
			if path == "/secure-only" {
				info, ok := oscore.RequestInfoFromContext(req.Context())
				if !ok || !info.Protected || string(info.RecipientID) != "client" {
					if err := w.SetResponse(codes.Unauthorized, message.TextPlain, nil); err != nil {
						t.Errorf("set OSCORE-required response: %v", err)
					}
					return
				}
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
			require.Fail(t, "TCP server did not stop")
		}
	}()

	secureClient, err := tcp.Dial(listener.Addr().String(),
		options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", true)))
	require.NoError(t, err)
	defer func() { require.NoError(t, secureClient.Close()) }()
	plaintextClient, err := tcp.Dial(listener.Addr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()

	assertTCPGetResponse(t, secureClient)
	assertTCPGetResponse(t, plaintextClient)
	for _, test := range []struct {
		name string
		conn *client.Conn
		code codes.Code
	}{
		{name: "protected", conn: secureClient, code: codes.Content},
		{name: "plaintext", conn: plaintextClient, code: codes.Unauthorized},
	} {
		t.Run("secure-only/"+test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request, err := test.conn.NewGetRequest(ctx, "/secure-only")
			require.NoError(t, err)
			defer test.conn.ReleaseMessage(request)
			response, err := test.conn.Do(request)
			require.NoError(t, err)
			defer test.conn.ReleaseMessage(response)
			require.Equal(t, test.code, response.Code())
		})
	}
}

func TestOSCORETCPErrorResponseIsUnprotectedAndDoesNotReachHandler(t *testing.T) {
	listener, err := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var handled atomic.Int32
	serverErrors := make(chan error, 4)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "other", false)),
		options.WithOSCOREErrorResponses(),
		options.WithErrors(func(err error) { serverErrors <- err }),
		options.WithHandlerFunc(func(*responsewriter.ResponseWriter[*client.Conn], *pool.Message) {
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
			require.Fail(t, "TCP OSCORE-error server did not stop")
		}
	}()

	cc, err := tcp.Dial(listener.Addr().String(),
		options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", false)))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := cc.Get(ctx, "/")
	require.NoError(t, err)
	require.Equal(t, codes.Unauthorized, response.Code())
	require.False(t, response.HasOption(message.OSCORE))
	require.Empty(t, response.Options())
	require.True(t, oscore.IsUnprotectedProcessingError(response.Context()))
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Empty(t, body)
	require.Zero(t, handled.Load())
	select {
	case serverErr := <-serverErrors:
		require.ErrorIs(t, serverErr, oscore.ErrNoSecurityContext)
	case <-ctx.Done():
		require.FailNow(t, "TCP server did not report original OSCORE processing error")
	}
}

func TestOSCORETCPRejectsMalformedTransportMessages(t *testing.T) {
	listener, err := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serverErrors := make(chan error, 4)
	handled := make(chan struct{}, 1)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", false)),
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
			require.Fail(t, "TCP malformed-message server did not stop")
		}
	}()

	cc, err := tcp.Dial(listener.Addr().String())
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()
	testOSCOREMalformedStreamMessages(t, cc, serverErrors, handled)
}

func testOSCOREMalformedStreamMessages(
	t *testing.T,
	cc *client.Conn,
	serverErrors <-chan error,
	handled <-chan struct{},
) {
	t.Helper()
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
				Code:    test.code,
				Token:   message.Token{byte(i + 1)},
				Options: test.options,
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

func TestOSCOREStreamNoResponseAndStrictPlaintextPolicy(t *testing.T) {
	t.Run("TCP", func(t *testing.T) {
		listener, listenerErr := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
		require.NoError(t, listenerErr)
		address := listener.Addr().String()
		testOSCOREStreamNoResponse(t, listener,
			func() (*client.Conn, error) { return tcp.Dial(address) },
			func() (*client.Conn, error) {
				return tcp.Dial(address, options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", false)))
			})
	})
	t.Run("TLS", func(t *testing.T) {
		serverConfig, clientConfig, _, err := createTLSConfig()
		require.NoError(t, err)
		listener, err := coapNet.NewTLSListener("tcp", "127.0.0.1:0", serverConfig)
		require.NoError(t, err)
		address := listener.Addr().String()
		testOSCOREStreamNoResponse(t, listener,
			func() (*client.Conn, error) { return tcp.Dial(address, options.WithTLS(clientConfig)) },
			func() (*client.Conn, error) {
				return tcp.Dial(address, options.WithTLS(clientConfig),
					options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", false)))
			})
	})
}

func TestOSCORETLSRejectsMalformedTransportMessages(t *testing.T) {
	serverConfig, clientConfig, _, err := createTLSConfig()
	require.NoError(t, err)
	listener, err := coapNet.NewTLSListener("tcp", "127.0.0.1:0", serverConfig)
	require.NoError(t, err)
	serverErrors := make(chan error, 4)
	handled := make(chan struct{}, 1)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", false)),
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
			require.Fail(t, "TLS malformed-message server did not stop")
		}
	}()

	cc, err := tcp.Dial(listener.Addr().String(), options.WithTLS(clientConfig))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()
	testOSCOREMalformedStreamMessages(t, cc, serverErrors, handled)
}

func testOSCOREStreamNoResponse(
	t *testing.T,
	listener tcpserver.Listener,
	dialPlain func() (*client.Conn, error),
	dialProtected func() (*client.Conn, error),
) {
	t.Helper()
	handled := make(chan struct{}, 2)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", false)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled <- struct{}{}
			if err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("suppressed"))); !errors.Is(err, noresponse.ErrMessageNotInterested) {
				t.Errorf("expected No-Response suppression, got %v", err)
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
			require.Fail(t, "TCP/TLS server did not stop")
		}
	}()

	plaintextClient, err := dialPlain()
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()
	protectedClient, err := dialProtected()
	require.NoError(t, err)
	defer func() { require.NoError(t, protectedClient.Close()) }()

	sendSuppressedRequest := func(cc *client.Conn) error {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		request, err := cc.NewGetRequest(ctx, "/oscore-noresponse")
		if err != nil {
			return err
		}
		request.SetOptionUint32(message.NoResponse, 2)
		defer cc.ReleaseMessage(request)
		_, err = cc.Do(request)
		return err
	}
	require.ErrorIs(t, sendSuppressedRequest(plaintextClient), context.DeadlineExceeded)
	select {
	case <-handled:
		t.Fatal("strict OSCORE policy dispatched a plaintext request")
	case <-time.After(30 * time.Millisecond):
	}

	require.ErrorIs(t, sendSuppressedRequest(protectedClient), context.DeadlineExceeded)
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("protected No-Response request was not dispatched")
	}
}

func TestOSCORETCPMultipleOutgoingContexts(t *testing.T) {
	listener, listenerErr := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	clientA, serverA := newTCPOSCOREContextPair(t, "ca", "sa")
	clientB, serverB := newTCPOSCOREContextPair(t, "cb", "sb")

	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts: []*oscore.Context{serverA, serverB},
	})
	require.NoError(t, err)
	server := tcp.NewServer(
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
			require.Fail(t, "TCP multi-context server did not stop")
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
	cc, err := tcp.Dial(listener.Addr().String(), options.WithOSCORE(processor))
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

func TestOSCORETCPObserveNotificationsAndCancellation(t *testing.T) {
	listener, err := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	masterSecret := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
		SenderSequenceNumber:         0xffffff,
	})
	require.NoError(t, err)
	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{serverContext}})
	require.NoError(t, err)
	sendNotifications := make(chan struct{})
	server := tcp.NewServer(
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
			require.Fail(t, "TCP Observe server did not stop")
		}
	}()

	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	cc, err := tcp.Dial(listener.Addr().String(), options.WithOSCORE(processor))
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	type observedResponse struct {
		body     string
		sequence uint64
		ok       bool
	}
	received := make(chan observedResponse, 3)
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
		sequence, ok := observationNet.AuthenticatedSequenceNumber(response.Context())
		received <- observedResponse{body: string(body), sequence: sequence, ok: ok}
	})
	require.NoError(t, err)
	initial := <-received
	require.Equal(t, "initial", initial.body)
	require.True(t, initial.ok)
	require.Equal(t, uint64(0xffffff), initial.sequence)
	close(sendNotifications)
	first := <-received
	require.Equal(t, "notification-1", first.body)
	require.True(t, first.ok)
	require.Equal(t, uint64(0x1000000), first.sequence)
	second := <-received
	require.Equal(t, "notification-2", second.body)
	require.True(t, second.ok)
	require.Equal(t, uint64(0x1000001), second.sequence)
	require.NoError(t, observation.Cancel(ctx))
}

func TestOSCORETLSObserveNotificationsAndCancellation(t *testing.T) {
	serverConfig, clientConfig, _, tlsConfigErr := createTLSConfig()
	require.NoError(t, tlsConfigErr)
	listener, listenerErr := coapNet.NewTLSListener("tcp", "127.0.0.1:0", serverConfig)
	require.NoError(t, listenerErr)
	sendNotification := make(chan struct{}, 1)
	cancelReceived := make(chan struct{}, 1)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", false)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			observe, observeErr := req.Observe()
			if observeErr != nil {
				t.Errorf("get Observe request option: %v", observeErr)
				return
			}
			switch observe {
			case 0:
				if err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("initial"))); err != nil {
					t.Errorf("set initial response: %v", err)
					return
				}
				w.Message().SetObserve(0)
				cc, token := w.Conn(), append(message.Token(nil), req.Token()...)
				go func() {
					<-sendNotification
					notification := cc.AcquireMessage(cc.Context())
					notification.SetCode(codes.Content)
					notification.SetToken(token)
					notification.SetObserve(1)
					notification.SetBody(bytes.NewReader([]byte("notification")))
					err := cc.WriteMessage(notification)
					cc.ReleaseMessage(notification)
					if err != nil {
						t.Errorf("write Observe notification: %v", err)
					}
				}()
			case 1:
				cancelReceived <- struct{}{}
				if err := w.SetResponse(codes.Content, message.TextPlain, nil); err != nil {
					t.Errorf("set cancellation response: %v", err)
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
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "TLS Observe server did not stop")
		}
	}()

	cc, err := tcp.Dial(
		listener.Addr().String(),
		options.WithTLS(clientConfig),
		options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", false)),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, cc.Close()) }()

	received := make(chan string, 2)
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
	receive := func(want string) {
		t.Helper()
		select {
		case got := <-received:
			require.Equal(t, want, got)
		case <-ctx.Done():
			require.FailNow(t, "timed out waiting for TLS Observe response", "wanted %q", want)
		}
	}
	receive("initial")
	sendNotification <- struct{}{}
	receive("notification")
	if err := observation.Cancel(ctx); err != nil {
		select {
		case <-cancelReceived:
			require.NoError(t, err, "cancellation request reached the server")
		default:
			require.NoError(t, err, "cancellation request did not reach the server")
		}
	}
}

func TestOSCORETCPClassicBlockwise(t *testing.T) {
	listener, listenerErr := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
	require.NoError(t, listenerErr)
	requestBody := bytes.Repeat([]byte("request-block-"), 500)
	responseBody := bytes.Repeat([]byte("response-block-"), 500)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", false)),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			got, readErr := req.ReadBody()
			if readErr != nil {
				t.Errorf("read assembled request: %v", readErr)
				return
			}
			if !bytes.Equal(requestBody, got) {
				t.Errorf("request body length %d, want %d", len(got), len(requestBody))
			}
			if err := w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(responseBody)); err != nil {
				t.Errorf("set response body: %v", err)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "TCP blockwise server did not stop")
		}
	}()

	cc, err := tcp.Dial(listener.Addr().String(),
		options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", false)),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
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

func TestOSCORETLSExchange(t *testing.T) {
	serverConfig, clientConfig, _, tlsConfigErr := createTLSConfig()
	require.NoError(t, tlsConfigErr)
	listener, listenerErr := coapNet.NewTLSListener("tcp", "127.0.0.1:0", serverConfig)
	require.NoError(t, listenerErr)
	server := tcp.NewServer(
		options.WithOSCORE(newTCPOSCOREProcessor(t, "server", "client", true)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
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
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "TLS server did not stop")
		}
	}()

	secureClient, err := tcp.Dial(
		listener.Addr().String(),
		options.WithTLS(clientConfig),
		options.WithOSCORE(newTCPOSCOREProcessor(t, "client", "server", true)),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, secureClient.Close()) }()
	plaintextClient, err := tcp.Dial(listener.Addr().String(), options.WithTLS(clientConfig))
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()
	assertTCPGetResponse(t, secureClient)
	assertTCPGetResponse(t, plaintextClient)
}

func assertTCPGetResponse(t *testing.T, cc *client.Conn) {
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

func newTCPOSCOREProcessor(t *testing.T, senderID, recipientID string, allowPlaintext bool) *oscore.Processor {
	t.Helper()
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SenderID:                     []byte(senderID),
		RecipientID:                  []byte(recipientID),
	})
	require.NoError(t, err)
	policy := oscore.PlaintextDisabled
	if allowPlaintext {
		policy = oscore.PlaintextAllowed
	}
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{securityContext},
		PlaintextPolicy: policy,
	})
	require.NoError(t, err)
	return processor
}

func newTCPOSCOREContextPair(t *testing.T, clientID, serverID string) (*oscore.Context, *oscore.Context) {
	t.Helper()
	masterSecret := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte(clientID),
		RecipientID:                  []byte(serverID),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte(serverID),
		RecipientID:                  []byte(clientID),
	})
	require.NoError(t, err)
	return clientContext, serverContext
}
