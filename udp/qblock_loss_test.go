package udp_test

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type qDropRelay struct {
	mu                sync.Mutex
	peer              *net.UDPAddr
	dropped           bool
	drops             map[int64]bool
	option            message.OptionID
	observed          map[int64]int
	fault             qRelayFault
	faultNum          int64
	faulted           bool
	q1Seen            map[int64]int
	q2ControlRequests int
	maxQ2ControlSize  int
	maxQ2ControlItems int
	dropForever       map[int64]bool
	reorderNUM        int64
	reorderPending    []byte
	reordered         bool
	reorderEnabled    bool
}

type qRelayFault uint8

const (
	qRelayNoFault qRelayFault = iota
	qRelayDropEmptyACK
	qRelayDropEmptyACKForever
	qRelayDuplicateEmptyACK
	qRelayDropFinalResponse
	qRelayDuplicateFinalResponse
)

func (r *qDropRelay) drop(data []byte) bool {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil {
		return false
	}
	if len(msg.Payload) == 0 ||
		(r.option == message.QBlock2 && msg.Code < codes.Created) ||
		(r.option == message.QBlock1 && msg.Code >= codes.Created) {
		return false
	}
	value, err := msg.Options.GetUint32(r.option)
	if err != nil {
		return false
	}
	szx, num, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil || szx > blockwise.SZX1024 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observed[num]++
	if r.dropForever[num] {
		r.dropped = true
		return true
	}
	if r.drops[num] {
		delete(r.drops, num)
		r.dropped = true
		return true
	}
	return false
}

func (r *qDropRelay) observeQ1Request(data []byte) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil || msg.Code >= codes.Created || len(msg.Payload) == 0 {
		return
	}
	value, err := msg.Options.GetUint32(message.QBlock1)
	if err != nil {
		return
	}
	_, num, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil {
		return
	}
	r.mu.Lock()
	r.q1Seen[num]++
	r.mu.Unlock()
}

func (r *qDropRelay) observeQ2Control(data []byte) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil || msg.Code >= codes.Created {
		return
	}
	items := 0
	for _, option := range msg.Options {
		if option.ID == message.QBlock2 {
			items++
		}
	}
	if items == 0 {
		return
	}
	r.mu.Lock()
	r.q2ControlRequests++
	r.maxQ2ControlSize = max(r.maxQ2ControlSize, len(data))
	r.maxQ2ControlItems = max(r.maxQ2ControlItems, items)
	r.mu.Unlock()
}

func (r *qDropRelay) applyResponseFault(data []byte) (drop, duplicate bool) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil {
		return false, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.faulted {
		return r.applyRepeatedResponseFaultLocked(msg)
	}
	if handled, drop, duplicate := r.applyEmptyACKFaultLocked(msg); handled {
		return drop, duplicate
	}
	return r.applyFinalResponseFaultLocked(msg)
}

func (r *qDropRelay) applyRepeatedResponseFaultLocked(msg message.Message) (bool, bool) {
	if r.fault == qRelayDropEmptyACKForever &&
		msg.Code == codes.Empty && msg.Type == message.Acknowledgement {
		return true, false
	}
	return false, false
}

func (r *qDropRelay) applyEmptyACKFaultLocked(msg message.Message) (bool, bool, bool) {
	if r.fault != qRelayDropEmptyACK && r.fault != qRelayDuplicateEmptyACK &&
		r.fault != qRelayDropEmptyACKForever {
		return false, false, false
	}
	if msg.Code != codes.Empty || msg.Type != message.Acknowledgement {
		return false, false, false
	}
	r.faulted = true
	return true, r.fault != qRelayDuplicateEmptyACK, r.fault == qRelayDuplicateEmptyACK
}

func (r *qDropRelay) applyFinalResponseFaultLocked(msg message.Message) (bool, bool) {
	if r.fault != qRelayDropFinalResponse && r.fault != qRelayDuplicateFinalResponse ||
		msg.Code < codes.Created || !msg.Options.HasOption(message.QBlock2) {
		return false, false
	}
	value, err := msg.Options.GetUint32(message.QBlock2)
	if err != nil {
		return false, false
	}
	_, num, more, err := blockwise.DecodeQBlockOption(value)
	if err != nil || num != r.faultNum || more {
		return false, false
	}
	r.faulted = true
	if r.fault == qRelayDuplicateFinalResponse {
		r.observed[num]++
	}
	return r.fault == qRelayDropFinalResponse, r.fault == qRelayDuplicateFinalResponse
}

func (r *qDropRelay) reorderResponse(data []byte) (forward, delayed []byte) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil ||
		msg.Code < codes.Created || len(msg.Payload) == 0 || !msg.Options.HasOption(message.QBlock2) {
		return data, nil
	}
	value, err := msg.Options.GetUint32(message.QBlock2)
	if err != nil {
		return data, nil
	}
	_, num, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil {
		return data, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reorderPending != nil {
		delayed = r.reorderPending
		r.reorderPending = nil
		r.reordered = true
		return data, delayed
	}
	if r.reorderEnabled && num == r.reorderNUM && !r.reordered {
		r.reorderPending = bytes.Clone(data)
		return nil, nil
	}
	return data, nil
}

func newQDropRelay(t *testing.T, target string, option message.OptionID, block int64, extra ...int64) (string, *qDropRelay) {
	t.Helper()
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	upstreamAddr, err := net.ResolveUDPAddr("udp4", target)
	require.NoError(t, err)
	upstream, err := net.DialUDP("udp4", nil, upstreamAddr)
	require.NoError(t, err)
	drops := map[int64]bool{block: true}
	for _, num := range extra {
		drops[num] = true
	}
	relay := &qDropRelay{
		drops: drops, option: option, observed: make(map[int64]int),
		q1Seen: make(map[int64]int), dropForever: make(map[int64]bool),
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		relay.forwardRequests(t, local, upstream)
	})
	wg.Go(func() {
		relay.forwardResponses(t, local, upstream)
	})
	t.Cleanup(func() {
		require.NoError(t, upstream.Close())
		require.NoError(t, local.Close())
		wg.Wait()
	})
	return local.LocalAddr().String(), relay
}

func (r *qDropRelay) forwardRequests(t *testing.T, local, upstream *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, peer, err := local.ReadFromUDP(buf)
		if err != nil {
			qRelayError(t, "read relay request", err)
			return
		}
		r.mu.Lock()
		r.peer = peer
		r.mu.Unlock()
		r.observeQ1Request(buf[:n])
		r.observeQ2Control(buf[:n])
		if !r.drop(buf[:n]) {
			if _, err := upstream.Write(buf[:n]); err != nil {
				qRelayError(t, "forward relay request", err)
				return
			}
		}
	}
}

func (r *qDropRelay) forwardResponses(t *testing.T, local, upstream *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, err := upstream.Read(buf)
		if err != nil {
			qRelayError(t, "read relay response", err)
			return
		}
		r.mu.Lock()
		peer := r.peer
		r.mu.Unlock()
		if peer == nil || r.drop(buf[:n]) {
			continue
		}
		drop, duplicate := r.applyResponseFault(buf[:n])
		if drop {
			continue
		}
		forward, delayed := r.reorderResponse(buf[:n])
		if forward == nil {
			continue
		}
		if _, err := local.WriteToUDP(forward, peer); err != nil {
			qRelayError(t, "forward relay response", err)
			return
		}
		if delayed != nil {
			if _, err := local.WriteToUDP(delayed, peer); err != nil {
				qRelayError(t, "forward reordered relay response", err)
				return
			}
		}
		if duplicate {
			if _, err := local.WriteToUDP(forward, peer); err != nil {
				qRelayError(t, "forward relay response", err)
				return
			}
		}
	}
}

func TestQBlockSelectiveRecovery(t *testing.T) {
	payload := qPayload()
	const szx = blockwise.SZX128
	last := int64((len(payload) - 1) / int(szx.Size()))
	for _, tt := range []struct {
		name   string
		option message.OptionID
		block  int64
	}{
		{"upload-first", message.QBlock1, 0},
		{"upload-middle", message.QBlock1, 3},
		{"upload-final", message.QBlock1, last},
		{"download-first", message.QBlock2, 0},
		{"download-middle", message.QBlock2, 3},
		{"download-final", message.QBlock2, last},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := new(atomic.Int32)
			uri := startQGoServer(t, false, szx, payload, new(qWireCounts), calls)
			addr, relay := newQDropRelay(t, strings.TrimPrefix(uri, "coap://"), tt.option, tt.block)
			cc := qDial(t, addr, false, szx, options.QBlockwiseOnly, new(qWireCounts))
			method := codes.GET
			var body []byte
			if tt.option == message.QBlock1 {
				method, body = codes.PUT, payload
			}
			resp := qDo(t, cc, method, "/q", body, message.NonConfirmable)
			wantCode := codes.Content
			if method == codes.PUT {
				wantCode = codes.Changed
			}
			require.Equal(t, wantCode, resp.Code())
			got, err := resp.ReadBody()
			require.NoError(t, err)
			require.True(t, bytes.Equal(payload, got), "exact assembled response must survive loss")
			require.Equal(t, int32(1), calls.Load())
			relay.mu.Lock()
			defer relay.mu.Unlock()
			require.True(t, relay.dropped, "test must actually drop a Q payload")
			require.Equal(t, 2, relay.observed[tt.block], "only missing block is retransmitted once")
			for num, count := range relay.observed {
				if num != tt.block {
					require.Equal(t, 1, count, "received block %d must not be retransmitted", num)
				}
			}
		})
	}
}

func TestQBlock2RecoveryRequestsFitConfiguredMTU(t *testing.T) {
	const (
		mtu        = 256
		blockCount = 100
	)
	payload := bytes.Repeat([]byte{0x3c}, blockCount*16)
	config := qblock.DefaultConfig()
	config.MaxPayloads = blockCount
	config.NonTimeout = 20 * time.Millisecond
	config.NonReceiveTimeout = 1100 * time.Millisecond
	config.NonMaxRetransmit = 2
	config.MaxLatency = 10 * time.Second
	calls := new(atomic.Int32)
	uri := startQGoServerWithMTU(t, false, blockwise.SZX16, payload,
		new(qWireCounts), calls, qGoServerOptions{mtu: mtu, blockwise: config})
	const droppedBlocks = 95
	dropped := make([]int64, droppedBlocks-1)
	for i := range dropped {
		dropped[i] = int64(i + 1)
	}
	addr, relay := newQDropRelay(t, strings.TrimPrefix(uri, "coap://"),
		message.QBlock2, 0, dropped...)
	cc, err := udp.Dial(addr,
		options.WithMTU(mtu),
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
		options.WithRequestMonitor(new(qWireCounts).monitor),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	response := qDo(t, cc, codes.GET, "/q", nil, message.NonConfirmable)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Positive(t, calls.Load())
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.True(t, relay.dropped)
	require.Greater(t, relay.q2ControlRequests, 1, "oversized missing list must split across requests")
	require.Greater(t, relay.maxQ2ControlItems, 1, "recovery requests should use repeated Q-Block2 options")
	require.Less(t, relay.maxQ2ControlItems, droppedBlocks)
	require.LessOrEqual(t, relay.maxQ2ControlSize, mtu)
}

func TestQBlock2OutOfOrderResponsesComplete(t *testing.T) {
	payload := qPayload()
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX128, payload,
		new(qWireCounts), new(atomic.Int32)), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock2, -1)
	relay.mu.Lock()
	relay.reorderEnabled = true
	relay.reorderNUM = 1
	relay.mu.Unlock()
	cc := qDial(t, addr, false, blockwise.SZX128, options.QBlockwiseOnly, new(qWireCounts))
	response := qDo(t, cc, codes.GET, "/q", nil, message.NonConfirmable)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.True(t, relay.reordered, "the relay must reverse two Q-Block2 payloads")
}

func TestQBlock2RecoveryExhaustionReturnsError(t *testing.T) {
	payload := qPayload()
	config := qblock.DefaultConfig()
	config.NonTimeout = 20 * time.Millisecond
	config.NonReceiveTimeout = 1100 * time.Millisecond
	config.NonMaxRetransmit = 1
	config.MaxLatency = 10 * time.Second
	calls := new(atomic.Int32)
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX128, payload,
		new(qWireCounts), calls, config), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock2, 3)
	relay.mu.Lock()
	relay.dropForever[3] = true
	relay.mu.Unlock()
	cc := qDial(t, addr, false, blockwise.SZX128, options.QBlockwiseOnly,
		new(qWireCounts), config)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := cc.NewGetRequest(ctx, "/q")
	require.NoError(t, err)
	request.SetType(message.NonConfirmable)
	_, err = cc.Do(request)
	cc.ReleaseMessage(request)
	require.ErrorContains(t, err, "Q-Block2 recovery exhausted")
	require.EqualValues(t, 1, calls.Load())
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.GreaterOrEqual(t, relay.observed[3], 2, "the unrecoverable Q-Block2 payload stays lost")
	require.GreaterOrEqual(t, relay.q2ControlRequests, 2, "send a missing-block recovery request")
	require.LessOrEqual(t, relay.q2ControlRequests, config.NonMaxRetransmit+2, "recovery requests must stay bounded")
}

func TestQBlock1ConfirmableACKExhaustionReturnsError(t *testing.T) {
	payload := qPayload()
	calls := new(atomic.Int32)
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX128, payload,
		new(qWireCounts), calls), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock1, -1)
	relay.mu.Lock()
	relay.fault = qRelayDropEmptyACKForever
	relay.mu.Unlock()
	cc, err := udp.Dial(addr,
		options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithTransmission(1, 20*time.Millisecond, 1, 1),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := cc.NewPutRequest(ctx, "/q", message.AppOctets, bytes.NewReader(payload))
	require.NoError(t, err)
	request.SetType(message.Confirmable)
	_, err = cc.Do(request)
	cc.ReleaseMessage(request)
	require.Error(t, err, "persistent empty-ACK loss must exhaust CoAP retransmissions")
	require.Zero(t, calls.Load(), "an incomplete Q1 upload must not reach the application")
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.True(t, relay.faulted)
	require.Equal(t, 2, relay.q1Seen[0], "one initial transmission and one configured retry")
}

type qBlockConfirmableFaultCase struct {
	name        string
	fault       qRelayFault
	finalNUM    bool
	retransmits int
}

func TestQBlockConfirmableACKAndFinalResponseFaults(t *testing.T) {
	tests := []qBlockConfirmableFaultCase{
		{name: "dropped-empty-ACK", fault: qRelayDropEmptyACK, retransmits: 2},
		{name: "duplicated-empty-ACK", fault: qRelayDuplicateEmptyACK, retransmits: 1},
		{name: "dropped-final-response", fault: qRelayDropFinalResponse, finalNUM: true, retransmits: 2},
		{name: "duplicated-final-response", fault: qRelayDuplicateFinalResponse, finalNUM: true, retransmits: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runQBlockConfirmableFaultCase(t, tt)
		})
	}
}

func runQBlockConfirmableFaultCase(t *testing.T, tt qBlockConfirmableFaultCase) {
	t.Helper()
	payload := bytes.Repeat([]byte{0x5a}, 47)
	config := qblock.DefaultConfig()
	config.MaxPayloads = 3
	calls := new(atomic.Int32)
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX16, payload,
		new(qWireCounts), calls, config), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock2, -1)
	relay.mu.Lock()
	relay.fault = tt.fault
	relay.faultNum = int64((len(payload) - 1) / int(blockwise.SZX16.Size()))
	relay.mu.Unlock()

	counts := new(qWireCounts)
	cc, err := udp.Dial(addr,
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
		options.WithRequestMonitor(counts.monitor),
		options.WithTransmission(1, 50*time.Millisecond, 3, 1),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	response := qDo(t, cc, codes.PUT, "/q", payload, message.Confirmable)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.EqualValues(t, 1, calls.Load(), "duplicate CON traffic must not redispatch the completed upload")
	assertQBlockConfirmableFaultCounts(t, relay, tt)
}

func assertQBlockConfirmableFaultCounts(t *testing.T, relay *qDropRelay, tt qBlockConfirmableFaultCase) {
	t.Helper()
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.True(t, relay.faulted, "the configured wire fault must be injected")
	if tt.finalNUM {
		assertFinalQBlockFaultCount(t, relay, tt)
	} else {
		require.Equal(t, tt.retransmits, relay.q1Seen[0],
			"only the Q1 block whose empty ACK was lost should be retransmitted")
	}
	assertQBlockQ1Counts(t, relay, tt)
	if tt.finalNUM {
		assertQBlockQ2Counts(t, relay)
	}
}

func assertFinalQBlockFaultCount(t *testing.T, relay *qDropRelay, tt qBlockConfirmableFaultCase) {
	finalCount := relay.observed[relay.faultNum]
	if tt.fault == qRelayDropFinalResponse {
		require.GreaterOrEqual(t, finalCount, tt.retransmits,
			"the dropped final Q2 payload must be retransmitted")
		require.LessOrEqual(t, finalCount, 5,
			"final Q2 retransmissions must stay within the default CoAP retry limit")
		return
	}
	require.Equal(t, tt.retransmits, finalCount,
		"only the final Q2 payload should be retransmitted or duplicated")
}

func assertQBlockQ1Counts(t *testing.T, relay *qDropRelay, tt qBlockConfirmableFaultCase) {
	for num, count := range relay.q1Seen {
		want := 1
		if !tt.finalNUM && num == 0 {
			want = tt.retransmits
		}
		require.Equal(t, want, count, "unexpected retry/duplicate for Q1 NUM %d", num)
	}
}

func assertQBlockQ2Counts(t *testing.T, relay *qDropRelay) {
	for num, count := range relay.observed {
		if num == relay.faultNum {
			continue
		}
		require.Equal(t, 1, count, "unexpected retry/duplicate for Q2 NUM %d", num)
	}
}
