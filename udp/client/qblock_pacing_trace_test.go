package client

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func (s *pairedQBlockSession) hasQueued() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) != 0
}

func TestQBlockPacingPairedPOSTPUTRepair(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{{"POST", codes.POST}, {"PUT", codes.PUT}} {
		t.Run(method.name, func(t *testing.T) {
			runQBlockPacingPairedRepair(t, method.code, &qblockPacingConfig{ProbingRate: 1024, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute)
		})
	}
	t.Run("default_rate_wait", func(t *testing.T) {
		config := qblock.DefaultManagerConfig()
		config.Transfer.MaxPayloads = 2
		config.Transfer.Lifetime = 10 * time.Minute
		h := newServerHarness(t, config, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
		})
		request := newPOSTWithBody(t, h.cc, message.Token{0xf2}, bytes.Repeat([]byte{'u'}, 48))
		defer h.cc.ReleaseMessage(request)
		prepared, err := h.cc.qblockClient.prepare(request, nil)
		require.NoError(t, err)
		require.True(t, prepared.OwnsTransmission)
		h.ingest(h.q1(t, 0xf3, 0, false, 4, "body"))
		require.Len(t, h.session.writesSnapshot(), 2)
		h.cc.qblockClient.mu.Lock()
		bodyDeadline, hasBodyDeadline := h.cc.qblockClient.manager.NextDeadline()
		h.cc.qblockClient.mu.Unlock()
		require.True(t, hasBodyDeadline)
		h.advance(bodyDeadline.Sub(h.now))
		require.Len(t, h.session.writesSnapshot(), 3)
		h.cc.qblockClient.mu.Lock()
		deadline, ready := h.cc.qblockClient.probeGate.nextDeadline()
		h.cc.qblockClient.mu.Unlock()
		require.True(t, ready)
		require.GreaterOrEqual(t, deadline.Sub(h.now), 100*time.Second)
		h.advance(deadline.Sub(h.now) - time.Nanosecond)
		require.Len(t, h.session.writesSnapshot(), 3)
		h.advance(time.Nanosecond)
		require.Len(t, h.session.writesSnapshot(), 5, "queued server body must start only at the default-rate deadline")
	})
}

func runQBlockPacingPairedRepair(t *testing.T, method codes.Code, pacing *qblockPacingConfig, lifetime time.Duration) {
	runQBlockPacingPairedRepairWithRelay(t, method, pacing, lifetime, nil)
}

func runQBlockPacingPairedRepairWithRelay(t *testing.T, method codes.Code, pacing *qblockPacingConfig, lifetime time.Duration, relay *qblocklink.Link) {
	t.Helper()
	start := time.Unix(100, 0)
	clientClock, serverClock := newFakeQBlockClock(start), newFakeQBlockClock(start)
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	config.Transfer.Lifetime = lifetime
	clientSession := &pairedQBlockSession{qblockTestSession: qblockTestSession{ctx: context.Background()}}
	serverSession := &pairedQBlockSession{qblockTestSession: qblockTestSession{ctx: context.Background()}}
	clientConfig, serverConfig := DefaultConfig, DefaultConfig
	clientConfig.BlockwiseEnable, serverConfig.BlockwiseEnable = false, false
	clientConfig.BlockwiseSZX, serverConfig.BlockwiseSZX = blockwise.SZX16, blockwise.SZX16
	var clientMID, serverMID, clientToken, serverToken atomic.Uint32
	serverMID.Store(100)
	serverToken.Store(100)
	clientConfig.GetMID = func() int32 { return int32(clientMID.Add(1)) }
	serverConfig.GetMID = func() int32 { return int32(serverMID.Add(1)) }
	clientConfig.GetToken = func() (message.Token, error) { return message.Token{byte(clientToken.Add(1))}, nil }
	serverConfig.GetToken = func() (message.Token, error) { return message.Token{byte(serverToken.Add(1))}, nil }
	var handlerCalls atomic.Int32
	serverConfig.Handler = func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		handlerCalls.Add(1)
		body, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{'u'}, 48), body)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	}
	client := NewConnWithOpts(clientSession, &clientConfig,
		withQBlockClient(qblockClientConfig{Manager: config, Clock: clientClock, ScheduleMode: qblockScheduleAutomatic, Pacing: pacing}),
		withQBlockServer(qblockServerConfig{Retention: 2 * config.Transfer.Lifetime}),
	)
	server := NewConnWithOpts(serverSession, &serverConfig,
		withQBlockClient(qblockClientConfig{Manager: config, Clock: serverClock, ScheduleMode: qblockScheduleAutomatic, Pacing: pacing}),
		withQBlockServer(qblockServerConfig{Retention: 2 * config.Transfer.Lifetime}),
	)
	t.Cleanup(clientSession.closeForTest)
	t.Cleanup(serverSession.closeForTest)
	require.True(t, client.qblockClient.automaticScheduling())
	require.True(t, server.qblockClient.automaticScheduling())

	original := message.Token{0xf1}
	require.NoError(t, client.claimToken(original, tokenOwnerRequest))
	defer client.releaseToken(original, tokenOwnerRequest)
	received := make(chan []byte, 1)
	_, loaded := client.tokenHandlerContainer.LoadOrStore(original.Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		body, err := io.ReadAll(msg.Body())
		if err == nil {
			received <- body
		}
		client.ReleaseMessage(msg)
	})
	require.False(t, loaded)
	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(method)
	request.SetToken(original)
	require.NoError(t, request.SetPath("/upload"))
	request.SetContentFormat(message.TextPlain)
	request.SetBody(bytes.NewReader(bytes.Repeat([]byte{'u'}, 48)))
	prepared, err := client.qblockClient.prepareQ1(request, nil)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)

	droppedUpload, droppedResponse := false, false
	delivered := make(map[uint64]int)
	acceptedServerTokens := make(map[string]struct{})
	for step := 0; step < 100; step++ {
		for turn := 0; turn < 100; turn++ {
			progressed := false
			if wire, ok := clientSession.pop(); ok {
				progressed = true
				if relay != nil {
					packets := processQBlockRelayWire(t, relay, qblocklink.ClientToServer, wire)
					if len(packets) == 0 {
						droppedUpload = true
					}
					for _, packet := range packets {
						deliverQBlockRelayPacket(t, server, packet)
						delivered[packet.ID]++
						acceptedServerTokens[string(wire.token)] = struct{}{}
					}
				} else if wire.options.HasOption(message.QBlock1) {
					value, getErr := wire.options.GetUint32(message.QBlock1)
					require.NoError(t, getErr)
					block, decodeErr := qblock.DecodeBlock(value)
					require.NoError(t, decodeErr)
					if block.Number == 1 && !droppedUpload {
						droppedUpload = true
					} else {
						deliverPairedQBlockWire(t, server, wire)
						acceptedServerTokens[string(wire.token)] = struct{}{}
					}
				} else {
					deliverPairedQBlockWire(t, server, wire)
					acceptedServerTokens[string(wire.token)] = struct{}{}
				}
			}
			if wire, ok := serverSession.pop(); ok {
				progressed = true
				if relay != nil {
					packets := processQBlockRelayWire(t, relay, qblocklink.ServerToClient, wire)
					if len(packets) == 0 {
						droppedResponse = true
					}
					for _, packet := range packets {
						deliverQBlockRelayPacket(t, client, packet)
						delivered[packet.ID]++
					}
				} else if wire.options.HasOption(message.QBlock2) {
					value, getErr := wire.options.GetUint32(message.QBlock2)
					require.NoError(t, getErr)
					block, decodeErr := qblock.DecodeBlock(value)
					require.NoError(t, decodeErr)
					if block.Number == 0 && !droppedResponse {
						droppedResponse = true
					} else {
						deliverPairedQBlockWire(t, client, wire)
					}
				} else {
					deliverPairedQBlockWire(t, client, wire)
				}
			}
			if !progressed {
				break
			}
		}
		select {
		case body := <-received:
			if relay != nil {
				for _, event := range relay.Trace() {
					want := 1
					if event.Action == qblocklink.Drop {
						want = 0
					}
					if event.Action == qblocklink.Duplicate {
						want = 2
					}
					require.Equal(t, want, delivered[event.ID], "actual endpoint deliveries input%d action%s", event.ID, event.Action)
				}
			}
			require.True(t, droppedUpload)
			require.True(t, droppedResponse)
			require.Equal(t, bytes.Repeat([]byte{'r'}, 48), body)
			require.EqualValues(t, 1, handlerCalls.Load())
			var responses int
			responseMIDs := make(map[int32]struct{})
			for _, wire := range serverSession.historySnapshot() {
				if !wire.options.HasOption(message.QBlock2) {
					continue
				}
				responses++
				_, accepted := acceptedServerTokens[string(wire.token)]
				require.True(t, accepted, "response must reuse an accepted request token")
				_, duplicateMID := responseMIDs[wire.mid]
				require.False(t, duplicateMID, "each NON response needs a fresh MID")
				responseMIDs[wire.mid] = struct{}{}
			}
			require.GreaterOrEqual(t, responses, 4, "initial response and repair must both be observed")
			return
		default:
		}
		require.Eventually(t, func() bool {
			if len(received) > 0 || clientSession.hasQueued() || serverSession.hasQueued() {
				return true
			}
			return advancePairedQBlockClock(client, server, clientClock, serverClock)
		}, time.Second, time.Millisecond)

	}
	t.Fatal("paced paired repair did not complete within 100 event turns")
}

func TestQBlockPacingScriptedGETRepair(t *testing.T) {
	now := time.Unix(100, 0)
	clock := newFakeQBlockClock(now)
	session := &qblockTestSession{ctx: context.Background()}
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{
		Manager: config, Clock: clock, ScheduleMode: qblockScheduleManual,
		Pacing: &qblockPacingConfig{ProbingRate: 1, MaxIntentBytes: 1 << 20},
	})
	token := message.Token{0xc7}
	req := newPrivateQBlockClientGET(t, cc, token)
	defer cc.ReleaseMessage(req)
	type result struct {
		msg *pool.Message
		err error
	}
	done := make(chan result, 1)
	go func() {
		msg, err := cc.doInternal(req)
		done <- result{msg: msg, err: err}
	}()
	require.Eventually(t, func() bool { return len(session.writesSnapshot()) == 1 }, time.Second, time.Millisecond)
	initial := session.writesSnapshot()[0]
	require.Equal(t, token, initial.token)
	require.True(t, initial.options.HasOption(message.QBlock2))

	fragment := newQBlockClientFragment(t, cc, token, 2, false, 48)
	fragment.SetType(message.NonConfirmable)
	fragment.SetMessageID(91)
	cc.ProcessReceivedMessageWithHandler(fragment, cc.handleReq)
	writes := session.writesSnapshot()
	require.Len(t, writes, 2)
	require.NotEqual(t, token, writes[1].token)
	first, err := qblock.DecodeBlock(writes[1].block)
	require.NoError(t, err)
	require.Equal(t, uint32(0), first.Number)
	cc.qblockClient.mu.Lock()
	deadline, hasDeadline := cc.qblockClient.probeGate.nextDeadline()
	cc.qblockClient.mu.Unlock()
	require.True(t, hasDeadline)
	require.True(t, deadline.After(clock.Now()))
	clock.Advance(deadline.Sub(clock.Now()) - time.Nanosecond)
	cc.qblockClient.Tick(clock.Now())
	require.Len(t, session.writesSnapshot(), 2)
	clock.Advance(time.Nanosecond)
	cc.qblockClient.Tick(clock.Now())
	writes = session.writesSnapshot()
	require.Len(t, writes, 3)
	require.NotEqual(t, writes[1].token, writes[2].token)
	second, err := qblock.DecodeBlock(writes[2].block)
	require.NoError(t, err)
	require.Equal(t, uint32(1), second.Number)

	for i, repairToken := range []message.Token{writes[1].token, writes[2].token} {
		response := newQBlockClientFragment(t, cc, repairToken, uint32(i), true, 48)
		response.SetType(message.NonConfirmable)
		response.SetMessageID(int32(92 + i))
		cc.ProcessReceivedMessageWithHandler(response, cc.handleReq)
	}
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.msg)
		body, readErr := io.ReadAll(got.msg.Body())
		require.NoError(t, readErr)
		require.Equal(t, bytes.Repeat([]byte{'a'}, 48), body)
		cc.ReleaseMessage(got.msg)
	case <-time.After(time.Second):
		t.Fatal("scripted GET did not complete after two paced repairs")
	}
	require.Len(t, session.writesSnapshot(), 3)
}

func TestQBlockPacingBidirectionalSilenceExpires(t *testing.T) {
	config := qblock.DefaultManagerConfig()
	config.Transfer.MaxPayloads = 2
	retention := 2 * config.Transfer.Lifetime
	type silentPeer struct {
		h     *serverHarness
		calls *int
	}
	peers := make([]silentPeer, 0, 2)
	for index := range 2 {
		calls := new(int)
		h := newServerHarness(t, config, qblockServerConfig{Retention: retention}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			(*calls)++
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
		})
		token := message.Token{byte(0xc0 + index)}
		req := newPOSTWithBody(t, h.cc, token, bytes.Repeat([]byte{'u'}, 48))
		defer h.cc.ReleaseMessage(req)
		prepared, err := h.cc.qblockClient.prepare(req, nil)
		require.NoError(t, err)
		require.True(t, prepared.OwnsTransmission)
		h.ingest(h.q1(t, byte(0xd0+index), 0, false, 4, "body"))
		require.Equal(t, 1, *calls)
		require.Len(t, h.session.writesSnapshot(), 2, "silent client upload owns the gate")
		h.cc.qblockClient.mu.Lock()
		slots := len(h.cc.qblockClient.workQueue.slots)
		h.cc.qblockClient.mu.Unlock()
		require.Equal(t, 2, slots)
		peers = append(peers, silentPeer{h: h, calls: calls})
	}
	for index, peer := range peers {
		peer.h.advance(config.Transfer.Lifetime)
		require.Zero(t, peer.h.snapshot().active)
		require.Len(t, peer.h.session.writesSnapshot(), 2)
		peer.h.cc.qblockClient.mu.Lock()
		slots := len(peer.h.cc.qblockClient.workQueue.slots)
		peer.h.cc.qblockClient.mu.Unlock()
		require.Zero(t, slots)
		peer.h.ingest(peer.h.q1(t, byte(0xe0+index), 0, false, 4, "body"))
		require.Equal(t, 1, *peer.calls, "suppression must survive an unsent response")
		peer.h.advance(retention - config.Transfer.Lifetime)
		state := peer.h.snapshot()
		require.Zero(t, state.records)
		require.Zero(t, state.managerTokens)
		require.Zero(t, state.serverTokens)
		require.Zero(t, state.managerBytes)
		require.Zero(t, state.reservations)
	}
}

// Sample deadlines only while both executors are idle. A published timer can
// lag a manager mutation or returning handler; advancing that stale timer would
// expire an exchange before the worker publishes its next response set.
func advancePairedQBlockClock(client, server *Conn, clientClock, serverClock *fakeQBlockClock) bool {
	a, b := client.qblockClient, server.qblockClient
	if !a.actionMu.TryLock() {
		return false
	}
	defer a.actionMu.Unlock()
	if !b.actionMu.TryLock() {
		return false
	}
	defer b.actionMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range []*qblockClient{a, b} {
		if c.server != nil {
			for _, r := range c.server.records {
				if r.handlerRunning {
					return false
				}
			}
		}
	}
	var earliest time.Time
	for i, c := range []*qblockClient{a, b} {
		clock := []*fakeQBlockClock{clientClock, serverClock}[i]
		deadline, ok := c.nextDeadlineLocked()
		if !ok {
			continue
		}
		if !clock.activeTimer() || !clock.deadline().Equal(deadline) {
			return false
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return false
	}
	clientClock.Advance(max(time.Duration(0), earliest.Sub(clientClock.Now())))
	serverClock.Advance(max(time.Duration(0), earliest.Sub(serverClock.Now())))
	return true
}

func TestQBlockPairedClockRejectsStalePublishedDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	aClock, bClock := newFakeQBlockClock(now), newFakeQBlockClock(now)
	a := newQBlockClockTestConnWithSession(t, &qblockTestSession{ctx: context.Background()}, qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: aClock})
	b := newQBlockClockTestConnWithSession(t, &qblockTestSession{ctx: context.Background()}, qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Clock: bClock})
	a.qblockClient.mu.Lock()
	require.True(t, a.qblockClient.gate().admit(1, qblockProbeControl, 0, now))
	require.True(t, a.qblockClient.gate().beginAttempt(1, 1))
	a.qblockClient.gate().settle(1, now)
	id, err := a.qblockClient.workQueue.reserve(64)
	require.NoError(t, err)
	require.NoError(t, a.qblockClient.workQueue.replace(id, qblockPendingWork{Kind: qblockWorkGET, ProbeKey: 2, Expires: now.Add(time.Hour)}, false))
	deadline, ok := a.qblockClient.nextDeadlineLocked()
	require.True(t, ok)
	a.qblockClient.mu.Unlock()
	aClock.NewTimer().Reset(deadline.Sub(now) + time.Hour)
	require.False(t, advancePairedQBlockClock(a, b, aClock, bClock))
	require.Equal(t, now, aClock.Now())
}
