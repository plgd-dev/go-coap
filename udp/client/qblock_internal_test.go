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
	"github.com/plgd-dev/go-coap/v3/net/oscore"
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
			MasterSecret: []byte("Q-Block OSCORE sizing test"),
			IDContext:    idContext,
			SenderID:     []byte("client"),
			RecipientID:  []byte("server"),
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
