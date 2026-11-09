package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestRetransmitCachedOSCOREResponse(t *testing.T) {
	const mid = 1234
	masterSecret := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)

	outerRequest, clientExchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{1},
		Type:      message.Confirmable,
		MessageID: mid,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(outerRequest)
	require.NoError(t, err)
	response, err := serverContext.ProtectResponse(message.Message{
		Code:      codes.Content,
		Token:     outerRequest.Token,
		Type:      message.Acknowledgement,
		MessageID: mid,
		Payload:   []byte("cached"),
	}, serverExchange)
	require.NoError(t, err)

	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cc := NewConnWithOpts(session, &cfg)
	cachedResponse := cc.AcquireMessage(context.Background())
	cachedResponse.SetMessage(response)
	require.NoError(t, cc.addResponseToCache(mid, cachedResponse))
	cc.ReleaseMessage(cachedResponse)

	retransmittedRequest := cc.AcquireMessage(context.Background())
	retransmittedRequest.SetMessage(outerRequest)
	require.NoError(t, cc.retransmitOSCOREResponse(retransmittedRequest))
	cc.ReleaseMessage(retransmittedRequest)

	got := session.lastWritten()
	require.NotNil(t, got)
	require.Equal(t, codes.Changed, got.Code())
	require.Equal(t, message.Acknowledgement, got.Type())
	require.Equal(t, int32(mid), got.MessageID())
	require.True(t, got.HasOption(message.OSCORE))
	body, err := got.ReadBody()
	require.NoError(t, err)
	require.True(t, bytes.Equal(response.Payload, body), "retransmitted ciphertext differs")

	opened, err := clientContext.UnprotectResponse(message.Message{
		Code:      got.Code(),
		Token:     got.Token(),
		Options:   got.Options(),
		Payload:   body,
		Type:      got.Type(),
		MessageID: got.MessageID(),
	}, clientExchange)
	require.NoError(t, err)
	require.Equal(t, []byte("cached"), opened.Payload)
}

func TestOSCOREObserveNotificationNumberRejectsOutOfOrder(t *testing.T) {
	const masterSecret = "oscore observe replay test secret"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: []byte(masterSecret),
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:         []byte(masterSecret),
		SenderID:             []byte("server"),
		RecipientID:          []byte("client"),
		SenderSequenceNumber: 10,
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	observeOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:    codes.GET,
		Token:   message.Token{1},
		Options: observeOptions,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(request)
	require.NoError(t, err)

	var notifications []message.Message
	for sequence := uint32(1); sequence <= 2; sequence++ {
		options, _, err := (message.Options{}).SetObserve(make([]byte, 4), sequence)
		require.NoError(t, err)
		notification, err := serverContext.ProtectResponseWithPartialIV(message.Message{
			Code:    codes.Content,
			Token:   request.Token,
			Options: options,
		}, serverExchange)
		require.NoError(t, err)
		notifications = append(notifications, notification)
	}

	cc := Conn{
		oscoreProcessor: processor,
		oscoreExchanges: map[string]udpOSCOREExchange{
			string(request.Token): {
				context:  clientContext,
				exchange: exchange,
				observe:  true,
			},
		},
	}
	deliver := func(notification message.Message) error {
		response := &pool.Message{}
		response.SetMessage(notification)
		return cc.unprotectIncomingResponse(response)
	}
	require.NoError(t, deliver(notifications[1]))
	require.ErrorIs(t, deliver(notifications[0]), oscore.ErrReplay)
	require.ErrorIs(t, deliver(notifications[1]), oscore.ErrReplay)
}

func TestDuplicateOSCOREConfirmableResponseIsAcknowledged(t *testing.T) {
	const mid = 1234
	masterSecret := []byte("duplicate OSCORE response test secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)
	observeOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	outerRequest, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{1},
		Options:   observeOptions,
		Type:      message.Confirmable,
		MessageID: 1,
	})
	require.NoError(t, err)
	_, serverExchange, err := serverContext.UnprotectRequest(outerRequest)
	require.NoError(t, err)
	notificationOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 1)
	require.NoError(t, err)
	notification, err := serverContext.ProtectResponseWithPartialIV(message.Message{
		Code:      codes.Content,
		Token:     outerRequest.Token,
		Options:   notificationOptions,
		Type:      message.Confirmable,
		MessageID: mid,
	}, serverExchange)
	require.NoError(t, err)
	wireMessage := pool.NewMessage(context.Background())
	wireMessage.SetMessage(notification)
	datagram, err := wireMessage.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)

	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cfg.Errors = func(err error) { require.NoError(t, err) }
	cc := NewConnWithOpts(session, &cfg)
	cc.oscoreExchanges[string(outerRequest.Token)] = udpOSCOREExchange{
		context:  clientContext,
		exchange: exchange,
		observe:  true,
	}
	ack := cc.AcquireMessage(context.Background())
	ack.SetCode(codes.Empty)
	ack.SetType(message.Acknowledgement)
	ack.SetMessageID(mid)
	require.NoError(t, cc.addResponseToCache(mid, ack))
	cc.ReleaseMessage(ack)

	first := cc.AcquireMessage(context.Background())
	first.SetMessage(notification)
	require.False(t, cc.processIncomingOSCOREMessage(first, datagram),
		"the first protected notification must continue to normal processing")
	cc.ReleaseMessage(first)

	duplicate := cc.AcquireMessage(context.Background())
	duplicate.SetMessage(notification)
	require.True(t, cc.processIncomingOSCOREMessage(duplicate, datagram),
		"an exact retransmission must be handled before application delivery")
	require.Equal(t, 1, session.writtenCount())
	written := session.lastWritten()
	require.NotNil(t, written)
	require.Equal(t, codes.Empty, written.Code())
	require.Equal(t, message.Acknowledgement, written.Type())
	require.Equal(t, int32(mid), written.MessageID())
	cc.ReleaseMessage(duplicate)
	require.NoError(t, cc.Close())
}

func TestProtectedRequestCachesProtectedResponseForRetransmission(t *testing.T) {
	const mid = 2345
	masterSecret := []byte("protected response cache test secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("client"),
		RecipientID:  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret: masterSecret,
		SenderID:     []byte("server"),
		RecipientID:  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{serverContext},
		RequireOSCORE: true,
	})
	require.NoError(t, err)
	const secret = "sensitive protected response"
	var handlerCalls atomic.Int32
	session := newRecordingSession()
	defer session.Close()
	cfg := DefaultConfig
	cfg.OSCORE = processor
	cfg.Errors = func(err error) { require.NoError(t, err) }
	cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		handlerCalls.Inc()
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(secret))))
	}
	cc := NewConnWithOpts(session, &cfg)
	defer cc.Close()

	protectedRequest, exchange, err := clientContext.ProtectRequest(message.Message{
		Code:      codes.GET,
		Token:     message.Token{0x42},
		Type:      message.Confirmable,
		MessageID: mid,
	})
	require.NoError(t, err)
	wireRequest := pool.NewMessage(context.Background())
	wireRequest.SetMessage(protectedRequest)
	datagram, err := wireRequest.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 1 }, time.Second, time.Millisecond)

	cachedResponse := cc.AcquireMessage(context.Background())
	ok, err := cc.getResponseFromCache(mid, cachedResponse)
	require.NoError(t, err)
	require.True(t, ok, "the protected response should be cached")
	require.True(t, cachedResponse.HasOption(message.OSCORE), "cache must contain the protected response")
	cachedBody, err := cachedResponse.ReadBody()
	require.NoError(t, err)
	require.NotEqual(t, []byte(secret), cachedBody, "plaintext response must never enter the retransmission cache")

	firstResponse := session.lastWritten()
	require.NotNil(t, firstResponse)
	require.True(t, firstResponse.HasOption(message.OSCORE))
	firstBody, err := firstResponse.ReadBody()
	require.NoError(t, err)
	opened, err := clientContext.UnprotectResponse(message.Message{
		Code:      firstResponse.Code(),
		Token:     firstResponse.Token(),
		Options:   firstResponse.Options(),
		Payload:   firstBody,
		Type:      firstResponse.Type(),
		MessageID: firstResponse.MessageID(),
	}, exchange)
	require.NoError(t, err)
	require.Equal(t, []byte(secret), opened.Payload)

	require.NoError(t, cc.Process(nil, datagram))
	require.Eventually(t, func() bool { return session.writtenCount() == 2 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, handlerCalls.Load(), "retransmitted request must not invoke the handler again")
	retransmittedResponse := session.lastWritten()
	require.NotNil(t, retransmittedResponse)
	require.True(t, retransmittedResponse.HasOption(message.OSCORE))
	retransmittedBody, err := retransmittedResponse.ReadBody()
	require.NoError(t, err)
	require.Equal(t, firstBody, retransmittedBody, "retransmission must return the original ciphertext")
	cc.ReleaseMessage(cachedResponse)
}
