package udp_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

// udpTestEDHOCMessage3 stands in for EDHOC message_3, a CBOR byte string.
var udpTestEDHOCMessage3 = []byte{0x43, 0x01, 0x02, 0x03}

type udpEDHOCHandledRequest struct {
	hasEDHOC    bool
	requestInfo oscore.RequestInfo
}

func startUDPEDHOCServer(t *testing.T, processor *oscore.Processor) (string, <-chan udpEDHOCHandledRequest) {
	t.Helper()
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	handled := make(chan udpEDHOCHandledRequest, 4)
	server := udp.NewServer(
		options.WithOSCORE(processor),
		options.WithOSCOREErrorResponses(),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
			requestInfo, _ := oscore.RequestInfoFromContext(r.Context())
			handled <- udpEDHOCHandledRequest{hasEDHOC: r.HasOption(message.EDHOC), requestInfo: requestInfo}
			if err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("protected"))); err != nil {
				t.Errorf("set response: %v", err)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "UDP EDHOC server did not stop")
		}
	})
	return listener.LocalAddr().String(), handled
}

// sendUDPCombinedRequest sends an RFC 9668 EDHOC + OSCORE request built from
// a request protected with clientContext over a connection without OSCORE.
func sendUDPCombinedRequest(
	ctx context.Context, t *testing.T, addr string, clientContext *oscore.Context,
) (*pool.Message, oscore.Exchange) {
	t.Helper()
	cc, err := udp.Dial(addr)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })
	protected, exchange, err := clientContext.ProtectRequest(message.Message{Code: codes.GET, Token: message.Token{0x21}})
	require.NoError(t, err)
	request := cc.AcquireMessage(ctx)
	t.Cleanup(func() { cc.ReleaseMessage(request) })
	request.SetCode(protected.Code)
	request.SetToken(protected.Token)
	request.SetType(message.Confirmable)
	request.SetMessageID(cc.GetMessageID())
	request.ResetOptionsTo(protected.Options.Add(message.Option{ID: message.EDHOC}))
	request.SetBody(bytes.NewReader(append(append([]byte(nil), udpTestEDHOCMessage3...), protected.Payload...)))
	response, err := cc.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { cc.ReleaseMessage(response) })
	return response, exchange
}

func TestOSCOREUDPIncomingRequestHookEstablishesContext(t *testing.T) {
	clientContext, serverContext := newUDPOSCOREContextPair(t, "client", "server")
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		PlaintextPolicy: oscore.PlaintextDisabled,
		IncomingRequestHook: func(processor *oscore.Processor, request message.Message) (message.Message, error) {
			if !request.Options.HasOption(message.EDHOC) {
				return request, nil
			}
			if !bytes.HasPrefix(request.Payload, udpTestEDHOCMessage3) {
				return message.Message{}, errors.New("malformed EDHOC + OSCORE payload")
			}
			if err := processor.AddContext(serverContext); err != nil {
				return message.Message{}, err
			}
			request.Options = request.Options.Remove(message.EDHOC)
			request.Payload = request.Payload[len(udpTestEDHOCMessage3):]
			return request, nil
		},
	})
	require.NoError(t, err)
	addr, handled := startUDPEDHOCServer(t, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, exchange := sendUDPCombinedRequest(ctx, t, addr, clientContext)
	require.True(t, response.HasOption(message.OSCORE), "combined request response must be OSCORE-protected")
	body, err := response.ReadBody()
	require.NoError(t, err)
	plaintextResponse, err := clientContext.UnprotectResponse(message.Message{
		Code:    response.Code(),
		Token:   response.Token(),
		Options: response.Options(),
		Payload: body,
	}, exchange)
	require.NoError(t, err)
	require.Equal(t, codes.Content, plaintextResponse.Code)
	require.Equal(t, []byte("protected"), plaintextResponse.Payload)
	select {
	case request := <-handled:
		require.False(t, request.hasEDHOC, "EDHOC option reached the handler")
		require.True(t, request.requestInfo.Protected)
		require.Equal(t, []byte("client"), request.requestInfo.RecipientID)
	case <-ctx.Done():
		require.FailNow(t, "combined request did not reach the handler")
	}

	clientProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{clientContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	require.NoError(t, err)
	secureClient, err := udp.Dial(addr, options.WithOSCORE(clientProcessor))
	require.NoError(t, err)
	defer func() { require.NoError(t, secureClient.Close()) }()
	getResponse, err := secureClient.Get(ctx, "/")
	require.NoError(t, err)
	require.Equal(t, codes.Content, getResponse.Code())
}

func TestOSCOREUDPUnprocessedEDHOCOptionIsBadOption(t *testing.T) {
	clientContext, serverContext := newUDPOSCOREContextPair(t, "client", "server")
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{serverContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	require.NoError(t, err)
	addr, handled := startUDPEDHOCServer(t, processor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	response, _ := sendUDPCombinedRequest(ctx, t, addr, clientContext)
	require.Equal(t, codes.BadOption, response.Code())
	require.False(t, response.HasOption(message.OSCORE))
	select {
	case <-handled:
		require.FailNow(t, "request with an unprocessed EDHOC option reached the handler")
	default:
	}
}
