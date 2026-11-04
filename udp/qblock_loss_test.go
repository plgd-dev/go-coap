package udp_test

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type qDropRelay struct {
	mu       sync.Mutex
	peer     *net.UDPAddr
	dropped  bool
	drops    map[int64]bool
	option   message.OptionID
	observed map[int64]int
}

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
	if err != nil || szx != blockwise.SZX128 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observed[num]++
	if r.drops[num] {
		delete(r.drops, num)
		r.dropped = true
		return true
	}
	return false
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
	relay := &qDropRelay{drops: drops, option: option, observed: make(map[int64]int)}
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
		if peer != nil && !r.drop(buf[:n]) {
			if _, err := local.WriteToUDP(buf[:n], peer); err != nil {
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
