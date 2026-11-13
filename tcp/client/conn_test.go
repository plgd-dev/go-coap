package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/tcp/coder"
	"github.com/stretchr/testify/require"
)

type tcpClientWriteRecorder struct {
	net.Conn
	writes [][]byte
}

func (c *tcpClientWriteRecorder) Write(p []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *tcpClientWriteRecorder) Read([]byte) (int, error) { return 0, io.EOF }
func (c *tcpClientWriteRecorder) Close() error             { return nil }
func (c *tcpClientWriteRecorder) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}
}

func (c *tcpClientWriteRecorder) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5683}
}
func (c *tcpClientWriteRecorder) SetDeadline(time.Time) error      { return nil }
func (c *tcpClientWriteRecorder) SetReadDeadline(time.Time) error  { return nil }
func (c *tcpClientWriteRecorder) SetWriteDeadline(time.Time) error { return nil }

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
				AcceptVolatileSenderSequence: true,
				AcceptReplayStateLoss:        true,
				MasterSecret:                 []byte("processing error response test"),
				SenderID:                     []byte("client"),
				RecipientID:                  []byte("server"),
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

func TestTCPOSCOREProtectsGeneratedBlockwiseRequestAfterCSM(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE Block1 continuation"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE Block1 continuation"),
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts: []*oscore.Context{securityContext},
	})
	require.NoError(t, err)
	transport := &tcpClientWriteRecorder{}
	sessionContext := context.Background()
	session := &Session{
		connection:          coapNet.NewConn(transport),
		messagePool:         pool.New(0, 0),
		maxMessageSize:      64,
		connectionCacheSize: 1,
	}
	session.ctx.Store(&sessionContext)
	cc := &Conn{
		session:                 session,
		oscoreProcessor:         processor,
		oscoreExchanges:         make(oscore.ExchangeTracker[tcpOSCOREExchange]),
		oscoreRequiredResponses: oscore.NewResponseRequirements(),
		blockwiseSZX:            blockwise.SZX16,
	}
	cc.blockWise = blockwise.New(cc, time.Minute, func(err error) {
		require.NoError(t, err)
	}, nil)

	csm := pool.NewMessage(context.Background())
	csm.SetCode(codes.CSM)
	csm.SetOptionBytes(message.TCPBlockWiseTransfer, nil)
	require.True(t, cc.handleSignals(csm))
	require.True(t, cc.peerBlockWiseTranferEnabled.Load(), "CSM must negotiate TCP Blockwise")
	cc.ReleaseMessage(csm)

	request := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.PUT)
	request.SetToken(message.Token("block1"))
	request.SetOptionBytes(message.URIPath, []byte("resource"))
	request.SetBody(bytes.NewReader(bytes.Repeat([]byte("payload"), 32)))
	require.NoError(t, cc.WriteMessage(request))
	require.Len(t, transport.writes, 1)

	firstBlock := message.Message{Options: make(message.Options, 0, 16)}
	_, err = coder.DefaultCoder.Decode(transport.writes[0], &firstBlock)
	require.NoError(t, err)
	require.Equal(t, codes.POST, firstBlock.Code, "OSCORE request uses outer POST code")
	require.True(t, firstBlock.Options.HasOption(message.OSCORE), "initial Block1 request must be protected")
	plaintextFirstBlock, _, err := serverContext.UnprotectRequest(firstBlock)
	require.NoError(t, err)
	firstBlockOption, err := plaintextFirstBlock.Options.GetUint32(message.Block1)
	require.NoError(t, err)

	continueResponse := cc.AcquireMessage(context.Background())
	continueResponse.SetCode(codes.Continue)
	continueResponse.SetToken(firstBlock.Token)
	continueResponse.SetOptionUint32(message.Block1, firstBlockOption)
	cc.ProcessReceivedMessageWithHandler(continueResponse, func(
		w *responsewriter.ResponseWriter[*Conn],
		r *pool.Message,
	) {
		cc.blockWise.Handle(
			w, r, cc.blockwiseSZX, session.maxMessageSize, cc.RemoteAddr(), cc.blockwiseHandle,
		)
	})

	require.Len(t, transport.writes, 2)
	continuation := message.Message{Options: make(message.Options, 0, 16)}
	_, err = coder.DefaultCoder.Decode(transport.writes[1], &continuation)
	require.NoError(t, err)
	require.Equal(t, codes.POST, continuation.Code, "OSCORE continuation uses outer POST code")
	require.True(t, continuation.Options.HasOption(message.OSCORE),
		"generated Block1 continuation must be protected after CSM negotiation")
}

func TestStaleOSCOREExchangeDoesNotRemoveReplacement(t *testing.T) {
	makeExchange := func(secret string) (*oscore.Context, oscore.Exchange) {
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			AcceptVolatileSenderSequence: true,
			AcceptReplayStateLoss:        true,
			MasterSecret:                 []byte(secret),
			SenderID:                     []byte("client"),
			RecipientID:                  []byte("server"),
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

type failingTCPOSCOREReplayStore struct {
	saveErr error
}

func (s failingTCPOSCOREReplayStore) Load() (oscore.ReplayWindowState, error) {
	return oscore.ReplayWindowState{}, nil
}

func (s failingTCPOSCOREReplayStore) Save(oscore.ReplayWindowState) error {
	return s.saveErr
}

func TestInternalTCPOSCOREReplayStoreFailureIsDropped(t *testing.T) {
	const masterSecret = "TCP internal replay store failure"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte(masterSecret),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	storeErr := errors.New("replay storage unavailable")
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		MasterSecret:                 []byte(masterSecret),
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
		ReplayWindowStore:            failingTCPOSCOREReplayStore{saveErr: storeErr},
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{serverContext}})
	require.NoError(t, err)

	reported := make(chan error, 1)
	serverConn, peerConn := net.Pipe()
	defer serverConn.Close()
	defer peerConn.Close()
	session := &Session{
		connection:  coapNet.NewConn(serverConn),
		messagePool: pool.New(0, 0),
		errors:      func(err error) { reported <- err },
	}
	cc := &Conn{oscoreProcessor: processor, oscoreErrorResponses: true, session: session}
	protected, _, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token{0x42},
	})
	require.NoError(t, err)
	request := pool.NewMessage(context.Background())
	request.SetMessage(protected)

	handled := make(chan struct{})
	go func() {
		defer close(handled)
		drop, handleErr := session.handleReceivedRequest(cc, request)
		if handleErr != nil || !drop {
			reported <- errors.New("failed to drop internally-faulted OSCORE request")
		}
	}()
	require.NoError(t, peerConn.SetReadDeadline(time.Now().Add(250*time.Millisecond)))
	var responseByte [1]byte
	_, readErr := peerConn.Read(responseByte[:])
	require.Error(t, readErr, "internal replay-store fault must not receive an OSCORE error response")
	select {
	case <-handled:
	case <-time.After(time.Second):
		require.FailNow(t, "TCP request processing did not finish")
	}
	select {
	case reportedErr := <-reported:
		require.ErrorIs(t, reportedErr, oscore.ErrInternalProcessing)
		require.ErrorIs(t, reportedErr, storeErr)
	case <-time.After(time.Second):
		require.FailNow(t, "internal replay-store fault was not reported")
	}
}

func TestFailedTCPOSCOREWriteDoesNotRollbackReplacement(t *testing.T) {
	makeExchange := func(secret string) (*oscore.Context, oscore.Exchange) {
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			AcceptVolatileSenderSequence: true,
			AcceptReplayStateLoss:        true,
			MasterSecret:                 []byte(secret),
			SenderID:                     []byte("client"),
			RecipientID:                  []byte("server"),
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
			AcceptVolatileSenderSequence: true,
			AcceptReplayStateLoss:        true,
			MasterSecret:                 []byte(secret),
			SenderID:                     []byte("client"),
			RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("failed TCP OSCORE cancellation write"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("expired TCP OSCORE exchange"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("expired TCP OSCORE processing error"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 secret,
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE consumer lifecycle"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE requirement consumer"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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

func TestPendingTCPOSCOREWriteKeepsExchangeAndRequirement(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("pending TCP OSCORE write"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	session := &Session{
		connection:  coapNet.NewConn(clientConn),
		messagePool: pool.New(1, 2048),
	}
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	cc := &Conn{
		session:                 session,
		oscoreProcessor:         processor,
		oscoreExchangeTimeout:   time.Second,
		oscoreNow:               func() time.Time { return now },
		oscoreExchanges:         make(oscore.ExchangeTracker[tcpOSCOREExchange]),
		oscoreRequiredResponses: oscore.NewResponseRequirements(),
	}
	request := pool.NewMessage(context.Background())
	request.SetCode(codes.GET)
	request.SetToken(message.Token("pw"))
	request.SetContext(context.WithValue(context.Background(), tcpOSCORERequestConsumerKey{}, true))

	writeResult := make(chan error, 1)
	go func() {
		writeResult <- cc.writeProtectedRequest(request)
	}()

	key := oscore.ExchangeTrackerKey(request.Token())
	require.Eventually(t, func() bool {
		cc.oscoreMu.Lock()
		_, exists := cc.oscoreExchanges[key]
		cc.oscoreMu.Unlock()
		if exists {
			return true
		}
		select {
		case writeErr := <-writeResult:
			require.NoError(t, writeErr, "protected write returned before exchange publication")
		default:
		}
		return false
	}, time.Second, time.Millisecond)

	now = now.Add(2 * time.Second)
	cc.oscoreMu.Lock()
	cc.expireTCPOSCOREExchangesLocked(now)
	cc.expireTCPOSCOREResponseRequirementsLocked(now)
	exchange, exchangeExists := cc.oscoreExchanges[key]
	requirements := cc.oscoreRequiredResponses[key]
	requirementExpiry, requirementExists := requirements[exchange.exchange]
	cc.oscoreMu.Unlock()
	require.True(t, exchangeExists, "in-flight request consumer must keep its exchange")
	require.True(t, requirementExists, "in-flight request consumer must keep OSCORE required")
	require.True(t, requirementExpiry.IsZero())

	response := pool.NewMessage(context.Background())
	response.SetMessage(message.Message{Code: codes.Content, Token: request.Token()})
	require.ErrorIs(t, cc.unprotectResponse(response), oscore.ErrOSCORERequired,
		"an unprotected success must not pass during the pending protected write")
	cc.oscoreMu.Lock()
	_, exchangeExists = cc.oscoreExchanges[key]
	cc.oscoreMu.Unlock()
	require.True(t, exchangeExists, "response processing must preserve the pending exchange")

	require.NoError(t, serverConn.Close(), "closing peer should unblock the pending write")
	require.Error(t, <-writeResult)
	require.NotContains(t, cc.oscoreRequiredResponses, key,
		"failed write must release its provisional response requirement")
}

func TestTCPOSCOREObservationExchangeSurvivesRequestConsumerRelease(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE Observe lifecycle"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE cancel observation"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
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
	require.NotContains(t, cc.oscoreExchanges, "token",
		"an Observe exchange with no deregistration request in flight should be removed")
	require.NotContains(t, cc.oscoreRequiredResponses["token"], observeExchange)
	require.Contains(t, cc.oscoreRequiredResponses["token"], otherExchange)
}

func TestCanceledTCPOSCOREObservationsExpireAfterBackstop(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE cancellation backstop"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	options, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)

	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	cc := &Conn{
		oscoreNow:               func() time.Time { return now },
		oscoreExchanges:         make(oscore.ExchangeTracker[tcpOSCOREExchange]),
		oscoreRequiredResponses: oscore.NewResponseRequirements(),
	}
	const cycleCount = 1000
	for i := range cycleCount {
		token := message.Token{byte(i >> 8), byte(i)}
		_, exchange, protectErr := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: token, Options: options,
		})
		require.NoError(t, protectErr)
		cancelOptions, _, cancelOptionsErr := (message.Options{}).SetObserve([]byte{0}, 1)
		require.NoError(t, cancelOptionsErr)
		_, cancelExchange, protectErr := securityContext.ProtectRequest(message.Message{
			Code: codes.GET, Token: token, Options: cancelOptions,
		})
		require.NoError(t, protectErr)
		key := oscore.ExchangeTrackerKey(token)
		cc.oscoreExchanges[key] = tcpOSCOREExchange{
			context:        securityContext,
			exchange:       exchange,
			observe:        true,
			cancelContext:  securityContext,
			cancelExchange: cancelExchange,
		}
		cc.oscoreRequiredResponses.Add(key, exchange, time.Time{})
		cc.oscoreRequiredResponses.Add(key, cancelExchange, time.Time{})
		cc.CancelObservation(token)
		require.Equal(t, now.Add(tcpOSCOREObserveCancelBackstop), cc.oscoreExchanges[key].expires)
	}
	require.Len(t, cc.oscoreExchanges, cycleCount)

	now = now.Add(tcpOSCOREObserveCancelBackstop)
	cc.oscoreMu.Lock()
	cc.expireTCPOSCOREExchangesLocked(now)
	cc.expireTCPOSCOREResponseRequirementsLocked(now)
	cc.oscoreMu.Unlock()
	require.Empty(t, cc.oscoreExchanges)
	require.Empty(t, cc.oscoreRequiredResponses)
}

func TestTCPOSCOREAcceptsDeregistrationResponseBeforeBackstop(t *testing.T) {
	const masterSecret = "TCP OSCORE cancellation response before backstop"
	clientContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte(masterSecret),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	serverContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte(masterSecret),
		SenderID:                     []byte("server"),
		RecipientID:                  []byte("client"),
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{clientContext}})
	require.NoError(t, err)

	const token = "tcp-cancel"
	registrationOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	_, clientObserveExchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token(token), Options: registrationOptions,
	})
	require.NoError(t, err)
	cancelOptions, _, err := (message.Options{}).SetObserve([]byte{0}, 1)
	require.NoError(t, err)
	cancelRequest, clientCancelExchange, err := clientContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token(token), Options: cancelOptions,
	})
	require.NoError(t, err)
	_, serverCancelExchange, err := serverContext.UnprotectRequest(cancelRequest)
	require.NoError(t, err)

	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	cc := &Conn{
		oscoreProcessor:         processor,
		oscoreNow:               func() time.Time { return now },
		oscoreExchanges:         make(oscore.ExchangeTracker[tcpOSCOREExchange]),
		oscoreRequiredResponses: oscore.NewResponseRequirements(),
	}
	key := oscore.ExchangeTrackerKey(message.Token(token))
	expires := cc.oscoreObserveCancelExpiry()
	cc.oscoreExchanges[key] = tcpOSCOREExchange{
		context:        clientContext,
		exchange:       clientObserveExchange,
		observe:        true,
		expires:        expires,
		cancelContext:  clientContext,
		cancelExchange: clientCancelExchange,
		cancelExpires:  expires,
	}
	cc.oscoreRequiredResponses.Add(key, clientCancelExchange, time.Time{})
	cc.CancelObservation(message.Token(token))
	require.Contains(t, cc.oscoreExchanges, key,
		"in-flight deregistration must retain its exchange until the response or backstop")
	response, err := serverContext.ProtectResponse(message.Message{
		Code:    codes.Content,
		Token:   message.Token(token),
		Payload: []byte("deregistered"),
	}, serverCancelExchange)
	require.NoError(t, err)
	responseMessage := pool.NewMessage(context.Background())
	responseMessage.SetMessage(response)

	require.NoError(t, cc.unprotectResponse(responseMessage))
	payload, err := responseMessage.ReadBody()
	require.NoError(t, err)
	require.Equal(t, []byte("deregistered"), payload)
	require.NotContains(t, cc.oscoreExchanges, key)
	require.NotContains(t, cc.oscoreRequiredResponses, key)
}

func TestTCPOSCOREExchangeExpiryRetainsActiveCancellationRequirement(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		AcceptVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("TCP OSCORE cancellation requirement expiry"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
	})
	require.NoError(t, err)
	registrationOptions, _, err := (message.Options{}).SetObserve(nil, 0)
	require.NoError(t, err)
	_, observeExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token("cancel"), Options: registrationOptions,
	})
	require.NoError(t, err)
	cancelOptions, _, err := (message.Options{}).SetObserve([]byte{0}, 1)
	require.NoError(t, err)
	_, cancelExchange, err := securityContext.ProtectRequest(message.Message{
		Code: codes.GET, Token: message.Token("cancel"), Options: cancelOptions,
	})
	require.NoError(t, err)

	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	key := oscore.ExchangeTrackerKey(message.Token("cancel"))
	cc := &Conn{
		oscoreNow: func() time.Time { return now },
		oscoreExchanges: oscore.ExchangeTracker[tcpOSCOREExchange]{
			key: {
				context:        securityContext,
				exchange:       observeExchange,
				observe:        true,
				expires:        now.Add(-time.Second),
				cancelContext:  securityContext,
				cancelExchange: cancelExchange,
				cancelExpires:  now.Add(time.Minute),
			},
		},
		oscoreRequiredResponses: oscore.ResponseRequirements{
			key: {cancelExchange: now.Add(time.Minute)},
		},
	}

	cc.oscoreMu.Lock()
	cc.expireTCPOSCOREExchangesLocked(now)
	cc.expireTCPOSCOREResponseRequirementsLocked(now)
	cc.oscoreMu.Unlock()
	_, exists := cc.oscoreExchanges[key]
	require.True(t, exists, "expired primary exchange must remain while cancellation response is required")

	exchange, exists := cc.oscoreExchangeForResponse(key)
	require.True(t, exists, "response lookup must preserve cancellation credentials")
	require.Equal(t, cancelExchange, exchange.cancelExchange)
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
