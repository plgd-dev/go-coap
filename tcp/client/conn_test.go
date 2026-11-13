package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestUnprotectedErrorResponseOnlyTerminatesOSCOREExchange(t *testing.T) {
	for _, test := range []struct {
		name                string
		code                codes.Code
		wantErr             error
		wantRetained        bool
		wantProcessingError bool
	}{
		{name: "decryption error", code: codes.BadRequest, wantProcessingError: true},
		{name: "unknown context", code: codes.Unauthorized, wantProcessingError: true},
		{name: "decode error", code: codes.BadOption, wantProcessingError: true},
		{name: "application not found", code: codes.NotFound, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "application server error", code: codes.InternalServerError, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "plaintext success", code: codes.Content, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			securityContext, err := oscore.NewContext(oscore.ContextConfig{
				AcceptReplayStateLoss: true,
				MasterSecret:          []byte("processing error response test"),
				SenderID:              []byte("client"),
				RecipientID:           []byte("server"),
			})
			require.NoError(t, err)
			request, exchange, err := securityContext.ProtectRequest(message.Message{
				Code: codes.GET, Token: message.Token{1},
			})
			require.NoError(t, err)
			key := string(request.Token)
			cc := &Conn{oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
				key: {
					context:  securityContext,
					exchange: exchange,
					expires:  time.Now().Add(time.Minute),
				},
			}}
			if test.wantRetained {
				cc.oscoreRequiredResponses = oscore.ResponseRequirements{
					key: {exchange: time.Time{}},
				}
			}
			response := pool.NewMessage(context.Background())
			response.SetMessage(message.Message{Code: test.code, Token: request.Token})

			err = cc.unprotectResponse(response)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			_, retained := cc.oscoreExchanges[key]
			require.Equal(t, test.wantRetained, retained)
			require.Equal(t, test.wantProcessingError, oscore.IsUnprotectedProcessingError(response.Context()))
		})
	}
}

func TestStaleOSCOREExchangeDoesNotRemoveReplacement(t *testing.T) {
	makeExchange := func(secret string) (*oscore.Context, oscore.Exchange) {
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          []byte(secret),
			SenderID:              []byte("client"),
			RecipientID:           []byte("server"),
		})
		require.NoError(t, err)
		_, exchange, err := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: message.Token{1},
		})
		require.NoError(t, err)
		return securityContext, exchange
	}

	oldContext, oldExchange := makeExchange("old response exchange context")
	newContext, newExchange := makeExchange("replacement response exchange")
	require.Equal(t, oldExchange.RequestPartialIVBytes(), newExchange.RequestPartialIVBytes())
	require.NotEqual(t, oldExchange, newExchange)

	const key = "\x01"
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {
				context:  newContext,
				exchange: newExchange,
				observe:  true,
			},
		},
	}
	stale := tcpOSCOREExchange{context: oldContext, exchange: oldExchange}

	require.ErrorIs(t, cc.updateTCPOSCOREExchangeResponse(
		key,
		stale,
		false,
		false,
	), oscore.ErrInvalidOSCOREMessage)
	cc.removeTCPOSCOREExchangeForProcessingError(key, oldExchange)

	current, exists := cc.oscoreExchanges[key]
	require.True(t, exists, "stale response processing must not remove the replacement exchange")
	require.Equal(t, newExchange, current.exchange)
}

func TestFailedTCPOSCOREWriteDoesNotRollbackReplacement(t *testing.T) {
	makeExchange := func(secret string) (*oscore.Context, oscore.Exchange) {
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          []byte(secret),
			SenderID:              []byte("client"),
			RecipientID:           []byte("server"),
		})
		require.NoError(t, err)
		_, exchange, err := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: message.Token{4},
		})
		require.NoError(t, err)
		return securityContext, exchange
	}

	_, failedExchange := makeExchange("failed TCP write")
	_, replacementExchange := makeExchange("replacement TCP write")
	require.NotEqual(t, failedExchange, replacementExchange)
	const key = "\x04"
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {exchange: replacementExchange},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {
				failedExchange:      time.Time{},
				replacementExchange: time.Time{},
			},
		},
	}

	cc.rollbackTCPOSCOREWrite(key, failedExchange, tcpOSCOREExchange{}, false, false)

	current, exists := cc.oscoreExchanges[key]
	require.True(t, exists)
	require.Equal(t, replacementExchange, current.exchange)
	require.NotContains(t, cc.oscoreRequiredResponses[key], failedExchange)
	require.Contains(t, cc.oscoreRequiredResponses[key], replacementExchange)
}

func TestFailedTCPOSCOREObserveCancellationRestoresOnlyOwnedExchange(t *testing.T) {
	makeExchange := func(secret string) oscore.Exchange {
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			AcceptReplayStateLoss: true,
			MasterSecret:          []byte(secret),
			SenderID:              []byte("client"),
			RecipientID:           []byte("server"),
		})
		require.NoError(t, err)
		_, exchange, err := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: message.Token{5},
		})
		require.NoError(t, err)
		return exchange
	}

	previousExchange := makeExchange("previous observe exchange")
	cancelExchange := makeExchange("observe cancellation exchange")
	const key = "\x05"
	previous := tcpOSCOREExchange{exchange: previousExchange, observe: true}
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {
				exchange:       previousExchange,
				observe:        true,
				cancelExchange: cancelExchange,
			},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {
				previousExchange: time.Time{},
				cancelExchange:   time.Time{},
			},
		},
	}

	cc.rollbackTCPOSCOREWrite(key, cancelExchange, previous, true, true)

	current, exists := cc.oscoreExchanges[key]
	require.True(t, exists)
	require.Equal(t, previousExchange, current.exchange)
	require.Empty(t, current.cancelExchange)
	require.Contains(t, cc.oscoreRequiredResponses[key], previousExchange)
	require.NotContains(t, cc.oscoreRequiredResponses[key], cancelExchange)
}

func TestFailedTCPOSCOREObserveCancellationPreservesPreviousCancellation(t *testing.T) {
	ctx := context.Background()
	const token = "token"
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("failed TCP OSCORE cancellation write"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts: []*oscore.Context{securityContext},
	})
	require.NoError(t, err)

	options, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	_, observeExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token(token), Options: options,
	})
	require.NoError(t, err)
	options, _, err = (message.Options{}).SetObserve([]byte{0}, 1)
	require.NoError(t, err)
	_, previousCancelExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token(token), Options: options,
	})
	require.NoError(t, err)

	clientConn, serverConn := net.Pipe()
	require.NoError(t, serverConn.Close())
	defer clientConn.Close()
	session := &Session{
		connection:  coapNet.NewConn(clientConn),
		messagePool: pool.New(1, 2048),
	}
	previous := tcpOSCOREExchange{
		context:        securityContext,
		exchange:       observeExchange,
		observe:        true,
		cancelContext:  securityContext,
		cancelExchange: previousCancelExchange,
		cancelExpires:  time.Now().Add(time.Minute),
	}
	cc := &Conn{
		session:         session,
		oscoreProcessor: processor,
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{token: previous},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			token: {
				observeExchange:        time.Time{},
				previousCancelExchange: time.Time{},
			},
		},
	}
	request := pool.NewMessage(ctx)
	request.SetMessage(message.Message{
		Code: codes.GET, Token: message.Token(token), Options: options,
	})
	request.SetContext(ctx)

	err = cc.writeProtectedRequest(request)
	require.Error(t, err)
	require.Equal(t, previous, cc.oscoreExchanges[token])
	require.Contains(t, cc.oscoreRequiredResponses[token], previousCancelExchange)
}

func TestExpiredTCPOSCOREExchangeStillRequiresProtection(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("expired TCP OSCORE exchange"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	request, exchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{2},
	})
	require.NoError(t, err)
	key := string(request.Token)
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {
				context:  securityContext,
				exchange: exchange,
				expires:  time.Now().Add(-time.Second),
			},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {exchange: time.Time{}},
		},
	}
	response := pool.NewMessage(context.Background())
	response.SetMessage(message.Message{Code: codes.Content, Token: request.Token})

	require.ErrorIs(t, cc.unprotectResponse(response), oscore.ErrOSCORERequired)
	require.False(t, oscore.IsUnprotectedProcessingError(response.Context()))
	require.Contains(t, cc.oscoreRequiredResponses[key], exchange)
}

func TestExpiredTCPOSCOREExchangeAllowsProcessingError(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("expired TCP OSCORE processing error"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	request, exchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{3},
	})
	require.NoError(t, err)
	key := string(request.Token)
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {
				context:  securityContext,
				exchange: exchange,
				expires:  time.Now().Add(-time.Second),
			},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {exchange: time.Time{}},
		},
	}
	response := pool.NewMessage(context.Background())
	response.SetMessage(message.Message{Code: codes.Unauthorized, Token: request.Token})

	require.NoError(t, cc.unprotectResponse(response))
	require.True(t, oscore.IsUnprotectedProcessingError(response.Context()))
	require.NotContains(t, cc.oscoreRequiredResponses, key)
}

func TestTCPOSCOREExchangeAcceptsResponseWithinConfiguredTimeout(t *testing.T) {
	secret := []byte("late TCP OSCORE response")
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          secret,
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          secret,
		SenderID:              []byte("server"),
		RecipientID:           []byte("client"),
	})
	require.NoError(t, err)
	request, exchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{7},
	})
	require.NoError(t, err)
	opened, serverExchange, err := serverContext.UnprotectRequest(request)
	require.NoError(t, err)
	protectedResponse, err := serverContext.ProtectResponse(message.Message{
		Code:  codes.Content,
		Token: request.Token,
	}, serverExchange)
	require.NoError(t, err)
	require.Equal(t, request.Token, protectedResponse.Token)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cc := &Conn{
		oscoreExchangeTimeout: 250 * time.Second,
		oscoreNow:             func() time.Time { return now },
	}
	expires := cc.oscoreExpiry()
	key := oscore.ExchangeTrackerKey(request.Token)
	cc.oscoreExchanges = oscore.ExchangeTracker[tcpOSCOREExchange]{
		key: {context: clientContext, exchange: exchange, expires: expires},
	}
	cc.oscoreRequiredResponses = oscore.ResponseRequirements{
		key: {exchange: expires},
	}
	now = now.Add(248 * time.Second)
	response := pool.NewMessage(context.Background())
	response.SetMessage(protectedResponse)
	require.NoError(t, cc.unprotectResponse(response))
	require.Equal(t, codes.Content, response.Code())
	require.Equal(t, codes.GET, opened.Code)
}

func TestTCPOSCORERequestConsumersReleaseExchanges(t *testing.T) {
	require.Zero(t, DefaultConfig.OSCOREExchangeTimeout)
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("TCP OSCORE consumer lifecycle"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	cc := &Conn{
		oscoreExchanges:         make(oscore.ExchangeTracker[tcpOSCOREExchange]),
		oscoreRequiredResponses: oscore.NewResponseRequirements(),
	}

	const requestCount = 10_000
	requests := make([]*pool.Message, 0, requestCount)
	for i := 0; i < requestCount; i++ {
		token := message.Token{
			byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i),
		}
		_, exchange, err := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: token,
		})
		require.NoError(t, err)
		key := oscore.ExchangeTrackerKey(token)
		cc.oscoreExchanges[key] = tcpOSCOREExchange{
			context:  securityContext,
			exchange: exchange,
		}
		cc.oscoreRequiredResponses[key] = map[oscore.Exchange]time.Time{
			exchange: time.Now().Add(time.Minute),
		}

		ctx, cancel := context.WithCancel(context.Background())
		req := pool.NewMessage(ctx)
		req.SetToken(token)
		req.SetContext(context.WithValue(ctx, tcpOSCOREResponseRequirementKey{}, exchange))
		cancel()
		requests = append(requests, req)
	}

	for _, req := range requests {
		cc.releaseTCPOSCOREResponseRequirement(req)
	}

	require.Empty(t, cc.oscoreExchanges)
	require.Empty(t, cc.oscoreRequiredResponses)
}

func TestTCPOSCORERequirementTracksLiveRequestConsumer(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("TCP OSCORE requirement consumer"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	request, exchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{6},
	})
	require.NoError(t, err)
	key := string(request.Token)
	req := pool.NewMessage(context.Background())
	req.SetToken(request.Token)
	req.SetContext(context.WithValue(context.Background(), tcpOSCOREResponseRequirementKey{}, exchange))
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {context: securityContext, exchange: exchange},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {exchange: time.Now().Add(time.Second)},
		},
	}

	cc.retainTCPOSCOREResponseRequirement(req)
	require.True(t, cc.oscoreRequiredResponses[key][exchange].IsZero(),
		"a live Do consumer must outlast the exchange-context timeout")
	cc.releaseTCPOSCOREResponseRequirement(req)
	require.NotContains(t, cc.oscoreRequiredResponses, key,
		"consumer completion must release the independent protection requirement")
	require.NotContains(t, cc.oscoreExchanges, key,
		"consumer completion must release the non-Observe exchange")
}

func TestTCPOSCOREObservationExchangeSurvivesRequestConsumerRelease(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("TCP OSCORE Observe lifecycle"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	options, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request, exchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token("observe"), Options: options,
	})
	require.NoError(t, err)
	key := oscore.ExchangeTrackerKey(request.Token)
	req := pool.NewMessage(context.Background())
	req.SetToken(request.Token)
	req.SetContext(context.WithValue(context.Background(), tcpOSCOREResponseRequirementKey{}, exchange))
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {context: securityContext, exchange: exchange, observe: true},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {exchange: time.Time{}},
		},
	}

	cc.releaseTCPOSCOREResponseRequirement(req)

	require.Contains(t, cc.oscoreExchanges, key,
		"an active Observe exchange must outlive the initial request consumer")
	require.NotContains(t, cc.oscoreRequiredResponses, key)
}

func TestCancelObservationClearsTCPOSCORERequirements(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptReplayStateLoss: true,
		MasterSecret:          []byte("TCP OSCORE cancel observation"),
		SenderID:              []byte("client"),
		RecipientID:           []byte("server"),
	})
	require.NoError(t, err)
	options, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	request, observeExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token("token"), Options: options,
	})
	require.NoError(t, err)
	_, otherExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: request.Token,
	})
	require.NoError(t, err)
	cc := &Conn{
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			"token": {exchange: observeExchange, observe: true},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			"token": {
				observeExchange: time.Time{},
				otherExchange:   time.Time{},
			},
		},
	}
	cc.CancelObservation(message.Token("token"))
	require.NotContains(t, cc.oscoreRequiredResponses["token"], observeExchange)
	require.Contains(t, cc.oscoreRequiredResponses["token"], otherExchange)
}

func TestConnReceiveCleanupPreservesOwnerAcquisition(t *testing.T) {
	ctx := context.Background()
	session := &Session{messagePool: pool.New(1, 2048)}
	session.ctx.Store(&ctx)
	cc := &Conn{session: session}
	for i := 0; i < 64; i++ {
		req := cc.AcquireMessage(ctx)
		req.SetCode(codes.Content)
		var owned *pool.Message
		cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			w.Message().SetModified(false)
			r.Hijack()
			cc.ReleaseMessage(r)
			owned = cc.AcquireMessage(ctx)
			owned.SetCode(codes.GET)
			owned.SetMessageID(42)
		})
		require.Equal(t, codes.GET, owned.Code(), "receive cleanup must not reset the owner's acquisition")
		require.Equal(t, int32(42), owned.MessageID())
		cc.ReleaseMessage(owned)
	}
}
