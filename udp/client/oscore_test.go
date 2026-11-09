package client

import (
	"bytes"
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/stretchr/testify/require"
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
