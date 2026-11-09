package dtls_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/noresponse"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestOSCOREDTLSExchange(t *testing.T) {
	serverDTLSConfig := testOSCOREDTLSServerOptions()
	listener, listenerErr := coapNet.NewDTLSListener("udp", "127.0.0.1:0", serverDTLSConfig)
	require.NoError(t, listenerErr)
	server := dtls.NewServer(
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "server", "client", true)),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			if req.Code() != codes.GET || req.HasOption(message.OSCORE) {
				if err := w.SetResponse(codes.BadRequest, message.TextPlain, nil); err != nil {
					t.Errorf("set bad-request response: %v", err)
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
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "DTLS server did not stop")
		}
	}()

	secureClient, err := dtls.Dial(
		listener.Addr().String(),
		testOSCOREDTLSClientOptions(),
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "client", "server", true)),
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, secureClient.Close()) }()
	plaintextClient, err := dtls.Dial(listener.Addr().String(), testOSCOREDTLSClientOptions())
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()

	for _, cc := range []*client.Conn{secureClient, plaintextClient} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		request, err := cc.NewGetRequest(ctx, "/oscore")
		require.NoError(t, err)
		response, err := cc.Do(request)
		cancel()
		require.NoError(t, err)
		require.Equal(t, codes.Content, response.Code())
		require.False(t, response.HasOption(message.OSCORE))
		body, err := response.ReadBody()
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
		cc.ReleaseMessage(request)
		cc.ReleaseMessage(response)
	}
}

func TestOSCOREDTLSRejectsMalformedTransportMessages(t *testing.T) {
	listener, err := coapNet.NewDTLSListener("udp", "127.0.0.1:0", testOSCOREDTLSServerOptions())
	require.NoError(t, err)
	serverErrors := make(chan error, 4)
	handled := make(chan struct{}, 1)
	server := dtls.NewServer(
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "server", "client")),
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
			require.Fail(t, "DTLS malformed-message server did not stop")
		}
	}()

	cc, err := dtls.Dial(listener.Addr().String(), testOSCOREDTLSClientOptions())
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
		{
			name:    "wrong outer request code",
			code:    codes.PUT,
			options: message.Options{{ID: message.OSCORE, Value: validOSCOREOption}},
		},
		{
			name: "Class E option outside ciphertext",
			code: codes.POST,
			options: message.Options{
				{ID: message.OSCORE, Value: validOSCOREOption},
				{ID: message.URIPath, Value: []byte("unexpected")},
			},
		},
	}
	for i, test := range malformedMessages {
		t.Run(test.name, func(t *testing.T) {
			request := cc.AcquireMessage(context.Background())
			request.SetMessage(message.Message{
				Code:    test.code,
				Type:    message.NonConfirmable,
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

func TestOSCOREDTLSNoResponseAndStrictPlaintextPolicy(t *testing.T) {
	listener, listenerErr := coapNet.NewDTLSListener("udp", "127.0.0.1:0", testOSCOREDTLSServerOptions())
	require.NoError(t, listenerErr)
	handled := make(chan struct{}, 2)
	server := dtls.NewServer(
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "server", "client")),
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
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "DTLS server did not stop")
		}
	}()

	plaintextClient, err := dtls.Dial(listener.Addr().String(), testOSCOREDTLSClientOptions())
	require.NoError(t, err)
	defer func() { require.NoError(t, plaintextClient.Close()) }()
	protectedClient, err := dtls.Dial(listener.Addr().String(), testOSCOREDTLSClientOptions(),
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "client", "server")))
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

func TestOSCOREDTLSObserveNotificationsAndCancellation(t *testing.T) {
	listener, listenerErr := coapNet.NewDTLSListener("udp", "127.0.0.1:0", testOSCOREDTLSServerOptions())
	require.NoError(t, listenerErr)
	sendNotifications := make(chan struct{})
	notificationsSent := make(chan error, 1)
	server := dtls.NewServer(
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "server", "client")),
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
					<-sendNotifications
					for sequence := uint32(1); sequence <= 2; sequence++ {
						notification := cc.AcquireMessage(cc.Context())
						notification.SetCode(codes.Content)
						notification.SetToken(token)
						notification.SetObserve(sequence)
						notification.SetBody(bytes.NewReader([]byte(fmt.Sprintf("notification-%d", sequence))))
						err := cc.WriteMessage(notification)
						cc.ReleaseMessage(notification)
						if err != nil {
							notificationsSent <- fmt.Errorf("write Observe notification: %w", err)
							return
						}
					}
					notificationsSent <- nil
				}()
			case 1:
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
			require.Fail(t, "DTLS Observe server did not stop")
		}
	}()

	cc, err := dtls.Dial(
		listener.Addr().String(),
		testOSCOREDTLSClientOptions(),
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "client", "server")),
	)
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
	select {
	case err := <-notificationsSent:
		require.NoError(t, err)
	case <-ctx.Done():
		require.FailNow(t, "DTLS Observe notifications were not acknowledged before cancellation")
	}
	require.NoError(t, observation.Cancel(ctx))
}

func TestOSCOREDTLSQBlock(t *testing.T) {
	requestBody := bytes.Repeat([]byte("dtls-qblock-oscore-request-"), 240)
	responseBody := bytes.Repeat([]byte("dtls-qblock-oscore-response-"), 240)
	listener, listenerErr := coapNet.NewDTLSListener("udp", "127.0.0.1:0", testOSCOREDTLSServerOptions())
	require.NoError(t, listenerErr)
	var handledUpload atomic.Bool
	server := dtls.NewServer(
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "server", "client")),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, 5*time.Second),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
			switch req.Code() {
			case codes.PUT:
				got, readErr := req.ReadBody()
				if readErr != nil {
					t.Errorf("read assembled DTLS Q-Block OSCORE upload: %v", readErr)
					return
				}
				if !bytes.Equal(requestBody, got) {
					t.Errorf("DTLS Q-Block OSCORE upload length %d, want %d", len(got), len(requestBody))
				}
				handledUpload.Store(true)
				if err := w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("uploaded"))); err != nil {
					t.Errorf("set upload response: %v", err)
				}
			case codes.GET:
				if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(responseBody)); err != nil {
					t.Errorf("set DTLS Q-Block OSCORE download response: %v", err)
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
		case err := <-serverDone:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			require.Fail(t, "DTLS Q-Block OSCORE server did not stop")
		}
	}()

	cc, err := dtls.Dial(
		listener.Addr().String(),
		testOSCOREDTLSClientOptions(),
		options.WithOSCORE(newDTLSOSCOREProcessor(t, "client", "server")),
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

func testOSCOREDTLSServerOptions() coapNet.DTLSServerOptions {
	return coapNet.NewDTLSServerOptions(
		piondtls.WithPSK(testOSCOREPSK),
		piondtls.WithPSKIdentityHint([]byte("go-coap OSCORE integration")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	)
}

func testOSCOREDTLSClientOptions() dtls.DTLSClientOptions {
	return dtls.NewDTLSClientOptions(
		piondtls.WithPSK(testOSCOREPSK),
		piondtls.WithPSKIdentityHint([]byte("go-coap OSCORE integration")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	)
}

func testOSCOREPSK([]byte) ([]byte, error) {
	return []byte{0xab, 0xc1, 0x23}, nil
}

func newDTLSOSCOREProcessor(t *testing.T, senderID, recipientID string, allowPlaintext ...bool) *oscore.Processor {
	t.Helper()
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SenderID:     []byte(senderID),
		RecipientID:  []byte(recipientID),
	})
	require.NoError(t, err)
	config := oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}, RequireOSCORE: true}
	if len(allowPlaintext) > 0 && allowPlaintext[0] {
		config.RequireOSCORE = false
		config.AllowUnprotected = true
	}
	processor, err := oscore.NewProcessor(config)
	require.NoError(t, err)
	return processor
}
