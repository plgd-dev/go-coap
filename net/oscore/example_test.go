package oscore_test

import (
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
)

func ExampleContext_directAPI() {
	// This example uses volatile sequence and replay state; never reuse these
	// settings with a production sender key.
	masterSecret := []byte("public test-only secret")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	if err != nil {
		panic(err)
	}
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 masterSecret,
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
	})
	if err != nil {
		panic(err)
	}
	clientProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{clientContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	if err != nil {
		panic(err)
	}
	serverProcessor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{serverContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	if err != nil {
		panic(err)
	}

	protectedRequest, exchange, err := clientProcessor.ProtectRequest(message.Message{
		Code:  codes.GET,
		Token: message.Token{1},
	})
	if err != nil {
		panic(err)
	}
	request, _, serverExchange, err := serverProcessor.ProcessIncomingRequest(protectedRequest)
	if err != nil {
		panic(err)
	}
	protectedResponse, err := serverContext.ProtectResponse(message.Message{
		Code:    codes.NotFound,
		Token:   request.Token,
		Payload: []byte("not found"),
	}, serverExchange)
	if err != nil {
		panic(err)
	}
	_, duplicateErr := serverContext.ProtectResponse(message.Message{
		Code:    codes.NotFound,
		Token:   request.Token,
		Payload: []byte("different plaintext"),
	}, serverExchange)
	response, err := clientProcessor.UnprotectResponse(protectedResponse, clientContext, exchange)
	if err != nil {
		panic(err)
	}
	fmt.Printf("authenticated payload: %s\n", response.Payload)
	fmt.Printf("duplicate protection rejected: %t\n", errors.Is(duplicateErr, oscore.ErrReplay))

	observeRequestOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	if err != nil {
		panic(err)
	}
	protectedObserveRequest, observeExchange, err := clientProcessor.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{2}, Options: observeRequestOptions,
	})
	if err != nil {
		panic(err)
	}
	observeRequest, _, serverObserveExchange, err := serverProcessor.ProcessIncomingRequest(protectedObserveRequest)
	if err != nil {
		panic(err)
	}
	observeResponseOptions, _, err := (message.Options{}).SetObserve(make([]byte, 4), 1)
	if err != nil {
		panic(err)
	}
	notificationsAccepted := true
	for range 2 {
		notification, protectErr := serverContext.ProtectResponseWithPartialIV(message.Message{
			Code: codes.Content, Token: observeRequest.Token,
			Options: observeResponseOptions, Payload: []byte("update"),
		}, serverObserveExchange)
		if protectErr != nil {
			panic(protectErr)
		}
		openedNotification, unprotectErr := clientProcessor.UnprotectResponse(
			notification, clientContext, observeExchange,
		)
		if unprotectErr != nil {
			panic(unprotectErr)
		}
		notificationsAccepted = notificationsAccepted &&
			openedNotification.Options.HasOption(message.Observe)
	}
	fmt.Printf("Observe notifications accepted: %t\n", notificationsAccepted)
	// Output:
	// authenticated payload: not found
	// duplicate protection rejected: true
	// Observe notifications accepted: true
}
