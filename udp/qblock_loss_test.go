package udp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockConfirmableConcurrentUploadsRespectNSTART(t *testing.T) {
	for _, nstart := range []int{1, 3} {
		t.Run(fmt.Sprintf("nstart-%d", nstart), func(t *testing.T) {
			expected := make(map[byte][]byte)
			for i := byte(1); i <= 3; i++ {
				expected[i] = bytes.Repeat([]byte{i}, 513+int(i))
			}
			var mu sync.Mutex
			calls := make(map[byte]int)
			router := mux.NewRouter()
			require.NoError(t, router.Handle("/upload", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				body, err := r.ReadBody()
				if err != nil {
					t.Errorf("read concurrent Q-Block upload: %v", err)
					return
				}
				if len(body) == 0 || !bytes.Equal(expected[body[0]], body) {
					t.Errorf("concurrent Q-Block upload body was mixed or unknown: %d bytes", len(body))
					return
				}
				mu.Lock()
				calls[body[0]]++
				mu.Unlock()
				if err := w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(nil)); err != nil {
					t.Errorf("set concurrent Q-Block response: %v", err)
				}
			})))
			listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			server := udp.NewServer(
				options.WithMux(router),
				options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
				options.WithQBlockwise(options.QBlockwiseOnly),
			)
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				require.NoError(t, listener.Close())
				require.NoError(t, <-served)
			})

			cc, err := udp.Dial(listener.LocalAddr().String(),
				options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
				options.WithQBlockwise(options.QBlockwiseOnly),
				options.WithTransmission(uint32(nstart), 20*time.Millisecond, 3, 1.0),
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, cc.Close()) })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			type result struct {
				err error
			}
			results := make(chan result, len(expected))
			var wg sync.WaitGroup
			for key, payload := range expected {
				key, payload := key, payload
				wg.Add(1)
				go func() {
					defer wg.Done()
					request, requestErr := cc.NewPutRequest(ctx, "/upload", message.AppOctets, bytes.NewReader(payload))
					if requestErr != nil {
						results <- result{err: requestErr}
						return
					}
					request.SetType(message.Confirmable)
					if requestErr = request.SetRequestTag([]byte{key}); requestErr != nil {
						cc.ReleaseMessage(request)
						results <- result{err: requestErr}
						return
					}
					response, doErr := cc.Do(request)
					cc.ReleaseMessage(request)
					if doErr == nil {
						if response.Code() != codes.Changed {
							doErr = fmt.Errorf("upload %d response code = %v", key, response.Code())
						}
						cc.ReleaseMessage(response)
					}
					results <- result{err: doErr}
				}()
			}
			wg.Wait()
			close(results)
			for result := range results {
				require.NoError(t, result.err)
			}
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, calls, len(expected))
			for key := range expected {
				require.Equal(t, 1, calls[key], "each independent upload must be dispatched exactly once")
			}
		})
	}
}

func TestQBlockConfirmableIndependentUploadsReorderRequestTags(t *testing.T) {
	for _, nstart := range []int{1, 3} {
		t.Run(fmt.Sprintf("nstart-%d", nstart), func(t *testing.T) {
			expected := map[byte][]byte{
				1: bytes.Repeat([]byte{1}, 513),
				2: bytes.Repeat([]byte{2}, 641),
			}
			var mu sync.Mutex
			calls := make(map[byte]int)
			router := mux.NewRouter()
			require.NoError(t, router.Handle("/upload", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				body, err := r.ReadBody()
				if err != nil {
					t.Errorf("read reordered Q-Block upload: %v", err)
					return
				}
				if len(body) == 0 || !bytes.Equal(expected[body[0]], body) {
					t.Errorf("reordered Q-Block upload body was mixed or unknown: %d bytes", len(body))
					return
				}
				mu.Lock()
				calls[body[0]]++
				mu.Unlock()
				if err := w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(nil)); err != nil {
					t.Errorf("set reordered Q-Block response: %v", err)
				}
			})))
			listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
			require.NoError(t, err)
			server := udp.NewServer(
				options.WithMux(router),
				options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
				options.WithQBlockwise(options.QBlockwiseOnly),
			)
			served := make(chan error, 1)
			go func() { served <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				require.NoError(t, listener.Close())
				require.NoError(t, <-served)
			})

			addr, relay := newQDropRelay(t, listener.LocalAddr().String(), message.QBlock1, -1)
			relay.mu.Lock()
			relay.reorderQ1BetweenTags = true
			relay.mu.Unlock()
			dial := func() *client.Conn {
				cc, dialErr := udp.Dial(addr,
					options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
					options.WithQBlockwise(options.QBlockwiseOnly),
					options.WithTransmission(uint32(nstart), 50*time.Millisecond, 3, 1.0),
				)
				require.NoError(t, dialErr)
				t.Cleanup(func() { require.NoError(t, cc.Close()) })
				return cc
			}
			clients := []*client.Conn{dial()}
			if nstart == 1 {
				clients = append(clients, dial())
				// The relay presents both clients as one upstream endpoint, so
				// their independent MID sequences must not overlap.
				firstMID := clients[0].GetMessageID()
				secondMID := clients[1].GetMessageID()
				distance := (firstMID + 32768 - secondMID) & 0xffff
				for range distance {
					clients[1].GetMessageID()
				}
			}

			type result struct {
				key byte
				err error
			}
			results := make(chan result, len(expected))
			start := make(chan struct{})
			var wg sync.WaitGroup
			index := 0
			for key, payload := range expected {
				key, payload := key, payload
				cc := clients[index%len(clients)]
				index++
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					request, requestErr := cc.NewPutRequest(ctx, "/upload", message.AppOctets, bytes.NewReader(payload))
					if requestErr != nil {
						results <- result{key: key, err: requestErr}
						return
					}
					request.SetType(message.Confirmable)
					requestErr = request.SetRequestTag([]byte{key})
					if requestErr != nil {
						cc.ReleaseMessage(request)
						results <- result{key: key, err: requestErr}
						return
					}
					response, doErr := cc.Do(request)
					cc.ReleaseMessage(request)
					if doErr == nil {
						if response.Code() != codes.Changed {
							doErr = fmt.Errorf("upload %d response code = %v", key, response.Code())
						}
						cc.ReleaseMessage(response)
					}
					results <- result{key: key, err: doErr}
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			for result := range results {
				require.NoError(t, result.err)
			}

			mu.Lock()
			require.Len(t, calls, len(expected))
			for key := range expected {
				require.Equal(t, 1, calls[key], "each independent upload must be dispatched exactly once")
			}
			mu.Unlock()

			relay.mu.Lock()
			defer relay.mu.Unlock()
			require.True(t, relay.reorderQ1Done, "relay must reorder first Q1 packets across Request-Tags")
			require.Len(t, relay.reorderQ1Order, 2)
			require.NotEqual(t, relay.reorderQ1Order[0], relay.reorderQ1Order[1])
			require.Len(t, relay.q1MaxInflight, len(clients), "track NSTART independently for each client")
			for peer, maximum := range relay.q1MaxInflight {
				require.LessOrEqual(t, maximum, nstart, "NSTART exceeded for client %s", peer)
				require.Positive(t, maximum, "client %s sent no Q1 CON request", peer)
			}
		})
	}
}

type qDropRelay struct {
	mu                      sync.Mutex
	peer                    *net.UDPAddr
	local                   *net.UDPConn
	peersByToken            map[string]*net.UDPAddr
	peersByMID              map[int32]*net.UDPAddr
	q1Inflight              map[string]map[int32]bool
	q1MaxInflight           map[string]int
	dropped                 bool
	drops                   map[int64]bool
	option                  message.OptionID
	observed                map[int64]int
	fault                   qRelayFault
	faultNum                int64
	faulted                 bool
	q1Seen                  map[int64]int
	q2ControlRequests       int
	maxQ2ControlSize        int
	maxQ2ControlItems       int
	dropForever             map[int64]bool
	reorderNUM              int64
	reorderPending          []byte
	reorderPendingDuplicate bool
	reordered               bool
	reorderEnabled          bool
	reorderOSCOREObserve    bool
	dropOSCOREObserve       int
	duplicateOSCOREObserve  bool
	duplicatedOSCOREObserve bool
	delayedACK              chan struct{}
	delayedACKStarted       time.Time
	emptyACKDelay           time.Duration
	delayStaleQ1            bool
	staleQ1Packet           []byte
	staleQ1Replayed         bool
	reorderQ1BetweenTags    bool
	reorderQ1HasFirstTag    bool
	reorderQ1FirstTag       string
	reorderQ1Pending        []byte
	reorderQ1PendingTag     string
	reorderQ1Order          []string
	reorderQ1Done           bool
	retainLateResponse      bool
	lateQ1Response          []byte
	lateQ1Peer              *net.UDPAddr
}

type qRelayFault uint8

const (
	qRelayNoFault qRelayFault = iota
	qRelayDropEmptyACK
	qRelayDropEmptyACKForever
	qRelayDuplicateEmptyACK
	qRelayDelayEmptyACK
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

func (r *qDropRelay) applyResponseFault(data []byte) (drop, duplicate bool, delay time.Duration) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil {
		return false, false, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.faulted {
		return r.applyRepeatedResponseFaultLocked(msg), false, 0
	}
	if handled, responseDrop, responseDuplicate, responseDelay := r.applyEmptyACKFaultLocked(msg); handled {
		return responseDrop, responseDuplicate, responseDelay
	}
	drop, duplicate = r.applyFinalResponseFaultLocked(msg)
	return drop, duplicate, 0
}

func (r *qDropRelay) applyRepeatedResponseFaultLocked(msg message.Message) bool {
	if r.fault == qRelayDropEmptyACKForever &&
		msg.Code == codes.Empty && msg.Type == message.Acknowledgement {
		return true
	}
	return false
}

func (r *qDropRelay) applyEmptyACKFaultLocked(msg message.Message) (bool, bool, bool, time.Duration) {
	if r.fault != qRelayDropEmptyACK && r.fault != qRelayDuplicateEmptyACK &&
		r.fault != qRelayDropEmptyACKForever && r.fault != qRelayDelayEmptyACK {
		return false, false, false, 0
	}
	if msg.Code != codes.Empty || msg.Type != message.Acknowledgement {
		return false, false, false, 0
	}
	r.faulted = true
	if r.fault == qRelayDelayEmptyACK {
		delay := r.emptyACKDelay
		if delay == 0 {
			delay = 100 * time.Millisecond
		}
		r.delayedACKStarted = time.Now()
		return true, true, false, delay
	}
	return true, r.fault != qRelayDuplicateEmptyACK, r.fault == qRelayDuplicateEmptyACK, 0
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

func (r *qDropRelay) reorderResponse(data []byte, duplicate bool) (
	forward, delayed []byte, duplicateForward, duplicateDelayed bool,
) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil ||
		msg.Code < codes.Created || len(msg.Payload) == 0 {
		return data, nil, duplicate, false
	}
	if msg.Options.HasOption(message.OSCORE) && msg.Options.HasOption(message.Observe) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.reorderOSCOREObserve && !r.reordered {
			if r.reorderPending != nil {
				delayed = r.reorderPending
				duplicateDelayed = r.reorderPendingDuplicate
				r.reorderPending = nil
				r.reorderPendingDuplicate = false
				r.reordered = true
				return data, delayed, duplicate, duplicateDelayed
			}
			r.reorderPending = bytes.Clone(data)
			r.reorderPendingDuplicate = duplicate
			return nil, nil, false, false
		}
		return data, nil, duplicate, false
	}
	if !msg.Options.HasOption(message.QBlock2) {
		return data, nil, duplicate, false
	}
	value, err := msg.Options.GetUint32(message.QBlock2)
	if err != nil {
		return data, nil, duplicate, false
	}

	_, num, _, err := blockwise.DecodeQBlockOption(value)
	if err != nil {
		return data, nil, duplicate, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reorderPending != nil {
		delayed = r.reorderPending
		duplicateDelayed = r.reorderPendingDuplicate
		r.reorderPending = nil
		r.reorderPendingDuplicate = false
		r.reordered = true
		return data, delayed, duplicate, duplicateDelayed
	}
	if r.reorderEnabled && num == r.reorderNUM && !r.reordered {
		r.reorderPending = bytes.Clone(data)
		r.reorderPendingDuplicate = duplicate
		return nil, nil, false, false
	}
	return data, nil, duplicate, false
}

func (r *qDropRelay) applyOSCOREObserveFault(data []byte) (drop, duplicate bool) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil ||
		!msg.Options.HasOption(message.OSCORE) || !msg.Options.HasOption(message.Observe) {
		return false, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dropOSCOREObserve > 0 {
		r.dropOSCOREObserve--
		return true, false
	}
	if r.duplicateOSCOREObserve && !r.duplicatedOSCOREObserve {
		r.duplicatedOSCOREObserve = true
		return false, true
	}
	return false, false
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
		delayedACK: make(chan struct{}),
		local:      local, peersByToken: make(map[string]*net.UDPAddr), peersByMID: make(map[int32]*net.UDPAddr),
		q1Inflight: make(map[string]map[int32]bool), q1MaxInflight: make(map[string]int),
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
		data := bytes.Clone(buf[:n])
		msg := message.Message{Options: make(message.Options, 0, 32)}
		_, decodeErr := coder.DefaultCoder.Decode(data, &msg)
		r.mu.Lock()
		r.peer = peer
		if decodeErr == nil {
			r.peersByMID[msg.MessageID] = peer
			if len(msg.Token) > 0 {
				r.peersByToken[string(msg.Token)] = peer
			}
			if msg.Type == message.Confirmable && msg.Options.HasOption(message.QBlock1) {
				r.trackQ1InflightLocked(peer, msg.MessageID)
			}
		}
		r.mu.Unlock()
		r.observeQ1Request(buf[:n])
		r.observeQ2Control(buf[:n])
		r.retainStaleQ1Packet(buf[:n])
		if r.drop(buf[:n]) {
			continue
		}
		packets := r.reorderQ1Request(data, msg, decodeErr == nil)
		for _, packet := range packets {
			if _, err := upstream.Write(packet); err != nil {
				qRelayError(t, "forward relay request", err)
				return
			}
		}
	}
}

func (r *qDropRelay) trackQ1InflightLocked(peer *net.UDPAddr, messageID int32) {
	peerKey := peer.String()
	inflight := r.q1Inflight[peerKey]
	if inflight == nil {
		inflight = make(map[int32]bool)
		r.q1Inflight[peerKey] = inflight
	}
	if inflight[messageID] {
		return
	}
	inflight[messageID] = true
	r.q1MaxInflight[peerKey] = max(r.q1MaxInflight[peerKey], len(inflight))
}

func (r *qDropRelay) acknowledgeQ1Locked(peer *net.UDPAddr, messageID int32) {
	if peer == nil {
		return
	}
	peerKey := peer.String()
	inflight := r.q1Inflight[peerKey]
	delete(inflight, messageID)
}

func (r *qDropRelay) reorderQ1Request(data []byte, msg message.Message, decoded bool) [][]byte {
	if !decoded || msg.Type != message.Confirmable || !msg.Options.HasOption(message.QBlock1) {
		return [][]byte{data}
	}
	start, end, err := msg.Options.Find(message.RequestTag)
	if err != nil || end-start != 1 {
		return [][]byte{data}
	}
	tag := string(msg.Options[start].Value)
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.reorderQ1BetweenTags || r.reorderQ1Done {
		return [][]byte{data}
	}
	if r.reorderQ1Pending == nil {
		if !r.reorderQ1HasFirstTag {
			r.reorderQ1FirstTag = tag
			r.reorderQ1HasFirstTag = true
		}
		if tag == r.reorderQ1FirstTag {
			r.reorderQ1Pending = bytes.Clone(data)
			r.reorderQ1PendingTag = tag
			return nil
		}
		return [][]byte{data}
	}
	if tag == r.reorderQ1PendingTag {
		return [][]byte{data}
	}
	r.reorderQ1Done = true
	r.reorderQ1Order = []string{tag, r.reorderQ1PendingTag}
	return [][]byte{data, r.reorderQ1Pending}
}

func (r *qDropRelay) forwardResponses(t *testing.T, local, upstream *net.UDPConn) {
	buf := make([]byte, 65535)
	for {
		n, err := upstream.Read(buf)
		if err != nil {
			qRelayError(t, "read relay response", err)
			return
		}
		msg := message.Message{Options: make(message.Options, 0, 32)}
		_, decodeErr := coder.DefaultCoder.Decode(buf[:n], &msg)
		r.mu.Lock()
		peer := r.peer
		if decodeErr == nil {
			if len(msg.Token) > 0 {
				if tokenPeer := r.peersByToken[string(msg.Token)]; tokenPeer != nil {
					peer = tokenPeer
				}
			} else if midPeer := r.peersByMID[msg.MessageID]; midPeer != nil {
				peer = midPeer
			}
		}
		r.mu.Unlock()
		if peer == nil {
			continue
		}
		r.retainLateQ1Response(buf[:n], peer, decodeErr == nil)
		if decodeErr == nil && r.dropRetainedLateResponse(msg) {
			switch msg.Type {
			case message.Acknowledgement:
				ack := make([]byte, 4)
				ackSize, err := coder.DefaultCoder.Encode(message.Message{
					Type: message.Acknowledgement, MessageID: msg.MessageID,
				}, ack)
				if err != nil {
					qRelayError(t, "encode dropped-response ACK", err)
					return
				}
				if _, err := local.WriteToUDP(ack[:ackSize], peer); err != nil {
					qRelayError(t, "forward dropped-response ACK", err)
					return
				}
			case message.Confirmable:
				ack := make([]byte, 4)
				ackSize, err := coder.DefaultCoder.Encode(message.Message{
					Type: message.Acknowledgement, MessageID: msg.MessageID,
				}, ack)
				if err != nil {
					qRelayError(t, "encode dropped-response ACK", err)
					return
				}
				if _, err := upstream.Write(ack[:ackSize]); err != nil {
					qRelayError(t, "acknowledge dropped late response", err)
					return
				}
			}
			continue
		}
		if r.drop(buf[:n]) {
			continue
		}
		if decodeErr == nil && msg.Type == message.Acknowledgement {
			r.mu.Lock()
			r.acknowledgeQ1Locked(peer, msg.MessageID)
			r.mu.Unlock()
		}
		drop, duplicate, delay := r.applyResponseFault(buf[:n])
		if delay > 0 {
			response, delayedPeer := bytes.Clone(buf[:n]), peer
			go func() {
				time.Sleep(delay)
				if _, err := local.WriteToUDP(response, delayedPeer); err != nil {
					qRelayError(t, "forward delayed relay response", err)
				}
				close(r.delayedACK)
			}()
			if drop {
				continue
			}
			duplicate = false
		}
		if drop {
			continue
		}
		oscoreDrop, oscoreDuplicate := r.applyOSCOREObserveFault(buf[:n])
		if oscoreDrop {
			continue
		}
		duplicate = duplicate || oscoreDuplicate
		forward, delayed, duplicateForward, duplicateDelayed := r.reorderResponse(buf[:n], duplicate)
		if forward == nil {
			continue
		}
		if _, err := local.WriteToUDP(forward, peer); err != nil {
			qRelayError(t, "forward relay response", err)
			return
		}
		if stale := r.replayStaleQ1AfterCompletion(buf[:n]); stale != nil {
			if _, err := upstream.Write(stale); err != nil {
				qRelayError(t, "replay stale Q1 block", err)
				return
			}
		}
		if delayed != nil {
			if _, err := local.WriteToUDP(delayed, peer); err != nil {
				qRelayError(t, "forward reordered relay response", err)
				return
			}
		}
		if duplicateForward {
			if _, err := local.WriteToUDP(forward, peer); err != nil {
				qRelayError(t, "forward relay response", err)
				return
			}
		}
		if duplicateDelayed {
			if _, err := local.WriteToUDP(delayed, peer); err != nil {
				qRelayError(t, "forward duplicated reordered relay response", err)
				return
			}
		}
	}
}

func (r *qDropRelay) dropRetainedLateResponse(msg message.Message) bool {
	if msg.Code < codes.Created {
		return false
	}
	if msg.Options.HasOption(message.QBlock2) {
		value, err := msg.Options.GetUint32(message.QBlock2)
		if err != nil {
			return false
		}
		_, _, more, err := blockwise.DecodeQBlockOption(value)
		if err != nil || more {
			return false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retainLateResponse
}

func (r *qDropRelay) retainLateQ1Response(data []byte, peer *net.UDPAddr, decoded bool) {
	if !decoded {
		return
	}
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil || msg.Code < codes.Created {
		return
	}
	if msg.Options.HasOption(message.QBlock2) {
		value, err := msg.Options.GetUint32(message.QBlock2)
		if err != nil {
			return
		}
		_, _, more, err := blockwise.DecodeQBlockOption(value)
		if err != nil || more {
			return
		}
	}
	r.mu.Lock()
	if r.retainLateResponse && r.lateQ1Response == nil {
		r.lateQ1Response = bytes.Clone(data)
		r.lateQ1Peer = peer
	}
	r.mu.Unlock()
}

func (r *qDropRelay) deliverLateQ1Response() error {
	r.mu.Lock()
	response, peer, local := bytes.Clone(r.lateQ1Response), r.lateQ1Peer, r.local
	r.mu.Unlock()
	if len(response) == 0 || peer == nil || local == nil {
		return errors.New("no retained late Q-Block1 response")
	}
	_, err := local.WriteToUDP(response, peer)
	return err
}

func (r *qDropRelay) retainStaleQ1Packet(data []byte) {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil ||
		msg.Code >= codes.Created || len(msg.Payload) == 0 || !msg.Options.HasOption(message.QBlock1) {
		return
	}
	r.mu.Lock()
	if r.delayStaleQ1 && r.staleQ1Packet == nil {
		r.staleQ1Packet = bytes.Clone(data)
	}
	r.mu.Unlock()
}

func (r *qDropRelay) replayStaleQ1AfterCompletion(data []byte) []byte {
	msg := message.Message{Options: make(message.Options, 0, 32)}
	if _, err := coder.DefaultCoder.Decode(data, &msg); err != nil ||
		msg.Code < codes.Created || !msg.Options.HasOption(message.QBlock2) {
		return nil
	}
	value, err := msg.Options.GetUint32(message.QBlock2)
	if err != nil {
		return nil
	}
	_, _, more, err := blockwise.DecodeQBlockOption(value)
	if err != nil || more {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.delayStaleQ1 || r.staleQ1Replayed || r.staleQ1Packet == nil {
		return nil
	}
	r.staleQ1Replayed = true
	return bytes.Clone(r.staleQ1Packet)
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

func TestQBlock1LateFinalResponseDoesNotReviveRetiredTransfer(t *testing.T) {
	firstPayload := bytes.Repeat([]byte{0x31}, 97)
	config := qblock.DefaultConfig()
	config.NonTimeout = 20 * time.Millisecond
	config.NonReceiveTimeout = 1100 * time.Millisecond
	config.NonMaxRetransmit = 1
	config.MaxLatency = 10 * time.Second
	var calls atomic.Int32
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/q", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		body, err := r.ReadBody()
		if err != nil {
			t.Errorf("read Q-Block upload body: %v", err)
			return
		}
		if !bytes.Equal(firstPayload, body) {
			t.Errorf("Q-Block upload body differs: got %d bytes, want %d", len(body), len(firstPayload))
			return
		}
		calls.Add(1)
		if err := w.SetResponse(codes.Changed, message.TextPlain, nil); err != nil {
			t.Errorf("set final Q-Block response: %v", err)
		}
	})))
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := udp.NewServer(
		options.WithMux(router),
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
	)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		require.NoError(t, <-served)
	})
	finalNUM := int64((len(firstPayload) - 1) / int(blockwise.SZX16.Size()))
	addr, relay := newQDropRelay(t, listener.LocalAddr().String(), message.QBlock1, -1)
	relay.mu.Lock()
	relay.retainLateResponse = true
	relay.mu.Unlock()
	cc, err := udp.Dial(addr,
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
		options.WithTransmission(1, 50*time.Millisecond, 1, 1),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := cc.NewPutRequest(ctx, "/q", message.AppOctets, bytes.NewReader(firstPayload))
	require.NoError(t, err)
	request.SetType(message.Confirmable)
	_, err = cc.Do(request)
	cc.ReleaseMessage(request)
	require.ErrorContains(t, err, "Q-Block1 final response recovery exhausted")
	require.EqualValues(t, 1, calls.Load(), "server must dispatch the accepted upload exactly once")

	relay.mu.Lock()
	require.NotEmpty(t, relay.lateQ1Response, "relay must retain the final response")
	require.GreaterOrEqual(t, relay.q1Seen[finalNUM], 2, "client must retry the final Q-Block1 block")
	relay.retainLateResponse = false
	relay.mu.Unlock()
	require.NoError(t, relay.deliverLateQ1Response())

	nextRequest, err := cc.NewPutRequest(ctx, "/q", message.AppOctets, bytes.NewReader(firstPayload))
	require.NoError(t, err)
	nextRequest.SetType(message.Confirmable)
	response, err := cc.Do(nextRequest)
	cc.ReleaseMessage(nextRequest)
	require.NoError(t, err, "a stale response must not complete or corrupt a subsequent transfer")
	body, err := response.ReadBody()
	cc.ReleaseMessage(response)
	require.NoError(t, err)
	require.Empty(t, body)
	require.EqualValues(t, 2, calls.Load(),
		"late traffic must not revive the retired transfer or duplicate application dispatch")
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

func TestQBlockConfirmableDelayedEmptyACK(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, 47)
	config := qblock.DefaultConfig()
	config.MaxPayloads = 3
	calls := new(atomic.Int32)
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX16, payload,
		new(qWireCounts), calls, config), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock2, -1)
	relay.mu.Lock()
	relay.fault = qRelayDelayEmptyACK
	relay.emptyACKDelay = 300 * time.Millisecond
	relay.mu.Unlock()
	cc, err := udp.Dial(addr,
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
		options.WithTransmission(1, 500*time.Millisecond, 3, 1.0),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	response := qDo(t, cc, codes.PUT, "/q", payload, message.Confirmable)
	require.Equal(t, codes.Changed, response.Code())
	select {
	case <-relay.delayedACK:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed empty ACK was not forwarded")
	}
	require.EqualValues(t, 1, calls.Load(), "late ACK traffic must not redispatch the completed upload")
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.GreaterOrEqual(t, time.Since(relay.delayedACKStarted), 250*time.Millisecond,
		"the injected empty ACK must be withheld for the configured delay")
	require.True(t, relay.faulted)
	require.Positive(t, relay.q1Seen[0], "the transfer must include a Q1 upload")
}

func TestQBlockConfirmableDelayedStaleBlockAfterCompletion(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, 47)
	config := qblock.DefaultConfig()
	config.MaxPayloads = 3
	calls := new(atomic.Int32)
	server := strings.TrimPrefix(startQGoServer(t, false, blockwise.SZX16, payload,
		new(qWireCounts), calls, config), "coap://")
	addr, relay := newQDropRelay(t, server, message.QBlock2, -1)
	relay.mu.Lock()
	relay.delayStaleQ1 = true
	relay.mu.Unlock()
	cc, err := udp.Dial(addr,
		options.WithBlockwise(true, blockwise.SZX16, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithQBlockwiseConfig(config),
		options.WithTransmission(1, 50*time.Millisecond, 3, 1.0),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })

	response := qDo(t, cc, codes.PUT, "/q", payload, message.Confirmable)
	require.Equal(t, codes.Changed, response.Code())
	require.Eventually(t, func() bool {
		relay.mu.Lock()
		defer relay.mu.Unlock()
		return relay.staleQ1Replayed
	}, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 1, calls.Load(), "a delayed stale Q1 packet must not redispatch a completed upload")
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
