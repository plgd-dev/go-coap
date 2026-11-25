package client

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	netclient "github.com/plgd-dev/go-coap/v3/net/client"
	"github.com/plgd-dev/go-coap/v3/net/observation"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type qContextSession struct {
	Session
	ctx context.Context
}

func (s qContextSession) Context() context.Context { return s.ctx }
func (s qContextSession) MaxMessageSize() uint32   { return 65536 }

func qControlMessage() *pool.Message {
	msg := pool.NewMessage(context.Background())
	msg.SetCode(codes.GET)
	msg.SetType(message.NonConfirmable)
	msg.SetMessageID(1)
	msg.SetToken([]byte("transfer"))
	msg.SetOptionUint32(message.QBlock2, 0)
	return msg
}

func TestQBlockProbingRateAndPeerProgress(t *testing.T) {
	cc := &Conn{session: qContextSession{ctx: context.Background()}}
	msg := qControlMessage()
	require.NoError(t, cc.qBlockProbeRate(msg))
	require.Greater(t, time.Until(cc.qControlNotBefore), time.Second)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	msg.SetContext(canceled)
	require.ErrorIs(t, cc.qBlockProbeRate(msg), context.Canceled)
	cc.qBlockPeerProgress()
	msg.SetContext(context.Background())
	require.NoError(t, cc.qBlockProbeRate(msg), "a response removes the unresponsive-peer gate")

	msg.SetCode(codes.PUT)
	msg.Remove(message.QBlock2)
	msg.SetOptionUint32(message.QBlock1, 8)
	msg.SetOptionUint32(message.Size1, 1024)
	require.NoError(t, msg.SetRequestTag([]byte("body-a")))
	require.NoError(t, cc.qBlockProbeRate(msg))
	require.Greater(t, time.Until(cc.qBodyNotBefore), 246*time.Second)
	require.Less(t, time.Until(cc.qBodyNotBefore), 248*time.Second)
	require.NoError(t, cc.qBlockProbeRate(msg), "a retransmitted NUM0 is not a new body")
	msg.SetOptionUint32(message.QBlock1, 24)
	require.NoError(t, cc.qBlockProbeRate(msg), "individual blocks are not charged as new bodies")
	msg.SetOptionUint32(message.QBlock1, 8)
	require.NoError(t, msg.SetRequestTag([]byte("body-b")))
	msg.SetContext(canceled)
	require.ErrorIs(t, cc.qBlockProbeRate(msg), context.Canceled)
}

func TestQBlockConfiguredProbingCapAndRouteCapacity(t *testing.T) {
	parameters := qblock.DefaultConfig()
	parameters.MaxPayloads, parameters.NonMaxRetransmit, parameters.MaxLatency = 64, 0, time.Second
	cc := &Conn{session: qContextSession{ctx: context.Background()}, qParameters: parameters}
	for range 100 {
		delay := cc.qBlockProbingDelay(1000)
		require.GreaterOrEqual(t, delay, 4*time.Second)
		require.Less(t, delay, 5*time.Second)
	}
	require.Equal(t, time.Second, cc.qBlockProbingDelay(1))
	exchange, err := cc.OpenQBlockExchange(context.Background(), []byte("configured"))
	require.NoError(t, err)
	defer exchange.Close()
	route, ok := exchange.(*qBlockRoute)
	require.True(t, ok)
	require.Equal(t, 128, cap(route.messages))
}

func TestQBlockAuthenticatedNotificationOrdering(t *testing.T) {
	now := time.Now()
	sequence := qBlockNotificationSequence{
		observe:          0x800000,
		authenticated:    0x800000,
		hasAuthenticated: true,
		lastEvent:        now,
	}
	response := pool.NewMessage(context.Background())
	response.SetObserve(0)
	observation.SetAuthenticatedSequenceNumber(response, 0x1000000)

	require.True(t, sequence.validNotification(response, now),
		"authenticated ordering accepts the next full-width sequence across the Observe wrap")

	response.SetContext(context.Background())
	require.False(t, sequence.validNotification(response, now),
		"plaintext notifications retain 24-bit Observe ordering")
}

func TestQBlockObserveRejectsUndersizedOSCOREReplayWindow(t *testing.T) {
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		UnsafeVolatileSenderSequence: true,
		AcceptReplayStateLoss:        true,
		MasterSecret:                 []byte("undersized Q-Block Observe replay window"),
		SenderID:                     []byte("client"),
		RecipientID:                  []byte("server"),
		QBlock2ObserveWindowSize:     64,
	})
	require.NoError(t, err)
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
	require.NoError(t, err)
	cc := newQRouteContextConn(t)
	cc.oscoreProcessor = processor
	cc.blockwiseSZX = blockwise.SZX1024
	cc.qDatagramLimit = 65535
	cc.qParameters = qblock.DefaultConfig()
	request := qControlMessage()
	require.ErrorIs(t, cc.prepareQObservation(request), oscore.ErrInvalidContext)
}

func TestQBlockDatagramBudgetIncludesOptionsAndToken(t *testing.T) {
	msg := qControlMessage()
	require.NoError(t, msg.SetPath("/long/option/path"))
	msg.SetBody(bytes.NewReader(bytes.Repeat([]byte{7}, 16)))
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	cc := &Conn{qDatagramLimit: uint32(len(wire) - 1)}
	require.ErrorIs(t, cc.validateQBlockDatagram(msg), blockwise.ErrQBlockTooLarge)
	cc.qDatagramLimit++
	require.NoError(t, cc.validateQBlockDatagram(msg))
}

func TestQBlockSZXAccountsForHeaderAndRecordBudget(t *testing.T) {
	cc := &Conn{
		session:        qContextSession{ctx: context.Background()},
		messagePool:    pool.New(0, 0),
		blockwiseSZX:   blockwise.SZX1024,
		qDatagramLimit: 512,
	}
	msg := qControlMessage()
	msg.SetCode(codes.PUT)
	msg.Remove(message.QBlock2)
	require.NoError(t, msg.SetPath("/"+string(bytes.Repeat([]byte{'a'}, 240))))
	szx, err := cc.qBlockRequestSZX(msg)
	require.NoError(t, err)
	require.LessOrEqual(t, szx, blockwise.SZX128)
	szx, err = cc.qBlockResponseSZX()
	require.NoError(t, err)
	require.Equal(t, blockwise.SZX256, szx)
	cc.qDatagramLimit = 12
	_, err = cc.qBlockRequestSZX(msg)
	require.ErrorIs(t, err, blockwise.ErrQBlockTooLarge)
	_, err = cc.qBlockResponseSZX()
	require.ErrorIs(t, err, blockwise.ErrQBlockTooLarge)
}

func TestQBlockRequestBudgetIncludesOSCOREIDContext(t *testing.T) {
	newProcessor := func(idContext []byte) *oscore.Processor {
		t.Helper()
		securityContext, err := oscore.NewContext(oscore.ContextConfig{
			UnsafeVolatileSenderSequence: true,
			AcceptReplayStateLoss:        true,
			MasterSecret:                 []byte("Q-Block OSCORE sizing test"),
			IDContext:                    idContext,
			SenderID:                     []byte("client"),
			RecipientID:                  []byte("server"),
		})
		require.NoError(t, err)
		processor, err := oscore.NewProcessor(oscore.ProcessorConfig{Contexts: []*oscore.Context{securityContext}})
		require.NoError(t, err)
		return processor
	}

	msg := qControlMessage()
	msg.SetCode(codes.PUT)
	msg.Remove(message.QBlock2)
	require.NoError(t, msg.SetPath("/data"))
	shortContext := &Conn{
		session:         qContextSession{ctx: context.Background()},
		messagePool:     pool.New(0, 0),
		blockwiseSZX:    blockwise.SZX1024,
		qDatagramLimit:  400,
		oscoreProcessor: newProcessor([]byte{1}),
	}
	longContext := &Conn{
		session:         qContextSession{ctx: context.Background()},
		messagePool:     pool.New(0, 0),
		blockwiseSZX:    blockwise.SZX1024,
		qDatagramLimit:  400,
		oscoreProcessor: newProcessor(bytes.Repeat([]byte{2}, 128)),
	}
	shortSZX, err := shortContext.qBlockRequestSZX(msg)
	require.NoError(t, err)
	longSZX, err := longContext.qBlockRequestSZX(msg)
	require.NoError(t, err)
	require.Less(t, longSZX, shortSZX, "long ID Context must reduce the request block size")

	msg.SetOptionUint32(message.QBlock1, 0)
	msg.SetOptionUint32(message.Size1, 128)
	require.NoError(t, msg.SetRequestTag([]byte("tag")))
	msg.SetBody(bytes.NewReader(bytes.Repeat([]byte{7}, 128)))
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	longContext.qDatagramLimit = uint32(len(wire) + 32)
	require.ErrorIs(t, longContext.validateQBlockDatagram(msg), blockwise.ErrQBlockTooLarge,
		"validation must include the selected context's larger OSCORE request option")

	longContext.qDatagramLimit = shortContext.qDatagramLimit
	shortResponseSZX, err := shortContext.qBlockResponseSZX()
	require.NoError(t, err)
	longResponseSZX, err := longContext.qBlockResponseSZX()
	require.NoError(t, err)
	require.Equal(t, shortResponseSZX, longResponseSZX, "response sizing must not include request ID Context")
}

func TestQBlockConnectionDeadlineCapacityAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cc := &Conn{session: qContextSession{ctx: ctx}}
	for i := 0; i < 128; i++ {
		require.NoError(t, cc.ScheduleQBlock(fmt.Sprint(i), time.Now().Add(time.Hour), func() error {
			t.Error("canceled job ran")
			return nil
		}))
	}
	require.ErrorIs(t, cc.ScheduleQBlock("overflow", time.Now(), func() error { return nil }), blockwise.ErrQBlockScheduleFull)
	cancel()
	require.Eventually(t, func() bool {
		cc.qJobsMu.Lock()
		defer cc.qJobsMu.Unlock()
		return len(cc.qJobs) == 0
	}, time.Second, time.Millisecond)
}

func TestQBlockBodyTokenLifetime(t *testing.T) {
	before := qBlockRetainedTokens.Load()
	cc := &Conn{session: qContextSession{ctx: context.Background()}, messagePool: pool.New(0, 0)}
	cc.Client = netclient.New[*Conn](cc, nil, message.GetToken, nil)
	exchange, err := cc.OpenQBlockExchange(context.Background(), []byte("original"))
	require.NoError(t, err)
	token, err := exchange.NewToken()
	require.NoError(t, err)
	require.NotEqual(t, message.Token("original"), token)
	require.Len(t, cc.qRoutes, 2)
	require.Equal(t, 1, cc.qActiveRoutes)
	require.Equal(t, before+2, qBlockRetainedTokens.Load())
	exchange.Close()
	exchange.Close()
	require.Empty(t, cc.qRoutes)
	require.Zero(t, cc.qActiveRoutes)
	require.Equal(t, before, qBlockRetainedTokens.Load())
}

func TestQBlockObservationRetainsTokenAcrossBodies(t *testing.T) {
	before := qBlockRetainedTokens.Load()
	cc := &Conn{session: qContextSession{ctx: context.Background()}, messagePool: pool.New(0, 0)}
	cc.Client = netclient.New[*Conn](cc, nil, message.GetToken, nil)
	token := message.Token("observe")
	exchange, err := cc.OpenQBlockExchange(context.Background(), token)
	require.NoError(t, err)
	route, ok := exchange.(*qBlockRoute)
	require.True(t, ok)
	for range 100 {
		_, err := exchange.NewToken()
		require.NoError(t, err)
		nextNotification := cc.AcquireMessage(context.Background())
		nextNotification.SetToken(token)
		route.messages <- nextNotification
		route.retireAliases(token)
		require.Len(t, cc.qRoutes, 1)
		require.Same(t, route, cc.qRoutes[string(token)])
		require.Equal(t, before+1, qBlockRetainedTokens.Load())
		require.Same(t, nextNotification, <-route.messages, "a queued newer notification must survive completion of its predecessor")
		cc.ReleaseMessage(nextNotification)
	}
	cc.CancelObservation(token)
	require.Empty(t, cc.qRoutes)
	require.Zero(t, cc.qActiveRoutes)
	require.Equal(t, before, qBlockRetainedTokens.Load())
}

func TestQBlockNotificationMIDBudget(t *testing.T) {
	before := qBlockRetainedTokens.Load()
	cc := &Conn{
		session: qContextSession{ctx: context.Background()}, messagePool: pool.New(0, 0),
		qBlockwiseMode: blockwise.QBlockwiseOnly,
	}
	cc.blockWise = blockwise.New(cc, time.Second, func(error) {}, nil)
	msg := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(msg)
	msg.SetCode(codes.Content)
	msg.SetType(message.NonConfirmable)
	msg.SetMessageID(100)
	msg.SetToken([]byte("observe"))
	msg.SetObserve(10)
	msg.SetOptionUint32(message.QBlock2, 0)
	require.NoError(t, cc.rememberQNotification(msg))
	require.NoError(t, cc.rememberQNotification(msg))
	require.Equal(t, before+1, qBlockRetainedTokens.Load(), "MID replacement cannot double-charge metadata")
	cc.forgetQNotificationToken(msg.Token())
	require.Equal(t, before, qBlockRetainedTokens.Load())
	require.NoError(t, cc.rememberQNotification(msg))
	cc.closeQBlocks()
	cc.closeQBlocks()
	require.Equal(t, before, qBlockRetainedTokens.Load())
}

func TestQBlockMulticastRequestsAreIgnored(t *testing.T) {
	cc := &Conn{session: qContextSession{ctx: context.Background()}, messagePool: pool.New(0, 0)}
	msg := qControlMessage()
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.NoError(t, cc.Process(&coapNet.ControlMessage{Dst: net.IPv4(224, 0, 1, 187)}, wire))
}

func TestQBlockObservationHijackedNotificationIsNotReleased(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := cc.NewObserveRequest(ctx, "/q")
	require.NoError(t, err)
	defer cc.ReleaseMessage(req)
	req.SetType(message.NonConfirmable)
	var owned *pool.Message
	registered := make(chan error, 1)
	go func() {
		_, observeErr := cc.DoObserve(req, func(notification *pool.Message) {
			notification.Hijack()
			owned = notification
		})
		registered <- observeErr
	}()
	require.Eventually(t, func() bool { return session.writtenCount() == 1 }, 5*time.Second, time.Millisecond)

	exchange, err := cc.OpenQBlockExchange(ctx, req.Token())
	require.NoError(t, err)
	exchange.Close()
	notification := cc.AcquireMessage(ctx)
	notification.SetCode(codes.Content)
	notification.SetToken(req.Token())
	notification.SetObserve(2)
	notification.SetBody(bytes.NewReader([]byte("notification")))
	_, _, keepRunning := cc.advanceQBlockObservation(req, blockwise.SZX1024, exchange, notification, qBlockNotificationSequence{})
	require.False(t, keepRunning)
	require.NoError(t, <-registered)

	require.Same(t, notification, owned)
	require.Equal(t, codes.Content, owned.Code())
	body, err := owned.ReadBody()
	require.NoError(t, err)
	require.Equal(t, "notification", string(body))
	cc.ReleaseMessage(owned)
}

func TestHandleResponseDoesNotMatchRequestCodeToPendingToken(t *testing.T) {
	cc, _ := newTestConnWithSession(t)
	ctx := context.Background()
	token := message.Token("pending")
	called := false
	cc.tokenHandlerContainer.Store(token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
		called = true
	})

	writer := responsewriter.New(cc.AcquireMessage(ctx), cc)
	defer cc.ReleaseMessage(writer.Message())
	request := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.POST)
	request.SetToken(token)
	cc.handleResponse(writer, request)
	require.False(t, called)
	_, exists := cc.tokenHandlerContainer.Load(token.Hash())
	require.True(t, exists, "a request code must not consume the pending response handler")

	response := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(response)
	response.SetCode(codes.Content)
	response.SetToken(token)
	cc.handleResponse(writer, response)
	require.True(t, called, "a response with the matching token must consume the handler")
}

func TestHandleResponseAcceptsSuccessCodeBoundary(t *testing.T) {
	cc, _ := newTestConnWithSession(t)
	ctx := context.Background()
	writer := responsewriter.New(cc.AcquireMessage(ctx), cc)
	defer cc.ReleaseMessage(writer.Message())

	t.Run("token", func(t *testing.T) {
		token := message.Token("pending")
		called := false
		cc.tokenHandlerContainer.Store(token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
			called = true
		})

		response := cc.AcquireMessage(ctx)
		defer cc.ReleaseMessage(response)
		response.SetCode(codes.Code(64))
		response.SetToken(token)
		cc.handleResponse(writer, response)

		require.True(t, called, "2.00 must dispatch to the matching pending token")
	})

	t.Run("tokenless", func(t *testing.T) {
		called := false
		remove, err := cc.tokenlessHandler.Register(func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
			called = true
		})
		require.NoError(t, err)
		defer remove(true)

		response := cc.AcquireMessage(ctx)
		defer cc.ReleaseMessage(response)
		response.SetCode(codes.Code(64))
		cc.handleResponse(writer, response)

		require.True(t, called, "2.00 must dispatch to the tokenless handler")
	})
}

func TestQBlockRequestsInAckOrResetAreIgnored(t *testing.T) {
	tests := []struct {
		name string
		typ  message.Type
		code codes.Code
		opt  message.OptionID
	}{
		{name: "ACK Q-Block2 request", typ: message.Acknowledgement, code: codes.GET, opt: message.QBlock2},
		{name: "RST Q-Block2 request", typ: message.Reset, code: codes.GET, opt: message.QBlock2},
		{name: "ACK Q-Block1 request", typ: message.Acknowledgement, code: codes.PUT, opt: message.QBlock1},
		{name: "RST Q-Block1 request", typ: message.Reset, code: codes.PUT, opt: message.QBlock1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, session := newTestConnWithSession(t)
			processed := false
			cc.requestMonitor = func(*Conn, *pool.Message) (bool, error) {
				processed = true
				return true, nil
			}
			req := qControlSessionMessage(tt.typ, tt.code, tt.opt)
			wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)

			require.NoError(t, cc.Process(nil, wire))
			require.False(t, processed, "request must not reach request processing")
			require.Zero(t, session.writtenCount(), "request must not receive a response")
		})
	}

	t.Run("ACK Block2 continuation", func(t *testing.T) {
		cc, _ := newTestConnWithSession(t)
		processed := false
		cc.requestMonitor = func(*Conn, *pool.Message) (bool, error) {
			processed = true
			return true, nil
		}
		req := qControlSessionMessage(message.Acknowledgement, codes.GET, message.Block2)
		wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
		require.NoError(t, err)

		require.NoError(t, cc.Process(nil, wire))
		require.True(t, processed, "legacy Block2 continuation must remain processable")
	})
}

func qControlSessionMessage(typ message.Type, code codes.Code, option message.OptionID) *pool.Message {
	msg := pool.NewMessage(context.Background())
	msg.SetType(typ)
	msg.SetCode(code)
	msg.SetMessageID(1)
	msg.SetToken([]byte("transfer"))
	msg.SetOptionUint32(option, 0)
	return msg
}
