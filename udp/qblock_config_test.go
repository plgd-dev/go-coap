package udp_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/require"
)

func TestQBlockConfiguredTransfers(t *testing.T) {
	payload := qPayload()
	for _, secure := range []bool{false, true} {
		for _, maxPayloads := range []int64{1, 3, 16, 64, 256} {
			t.Run(fmt.Sprintf("secure=%v/max=%d", secure, maxPayloads), func(t *testing.T) {
				config := qblock.DefaultConfig()
				config.MaxPayloads = maxPayloads
				config.NonTimeout, config.NonReceiveTimeout = 20*time.Millisecond, 1100*time.Millisecond
				config.NonMaxRetransmit, config.MaxLatency = 2, 10*time.Second
				calls := new(atomic.Int32)
				counts := new(qWireCounts)
				uri := startQGoServer(t, secure, blockwise.SZX128, payload, new(qWireCounts), calls, config)
				cc := qDial(t, strings.TrimPrefix(strings.TrimPrefix(uri, "coaps://"), "coap://"),
					secure, blockwise.SZX128, options.QBlockwiseAuto, counts, config)
				for _, upload := range []bool{false, true} {
					method := codes.GET
					var body []byte
					if upload {
						method, body = codes.PUT, payload
					}
					response := qDo(t, cc, method, "/q", body, message.NonConfirmable)
					got, err := response.ReadBody()
					require.NoError(t, err)
					require.Equal(t, payload, got)
				}
				require.EqualValues(t, 2, calls.Load())
				require.Positive(t, counts.q2.Load(), "Auto must select Q, not pass via fallback")
			})
		}
	}
}

func TestQBlockConfiguredSinglePayloadSetsWithoutRecovery(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			config := qblock.DefaultConfig()
			config.MaxPayloads, config.NonMaxRetransmit = 1, 0
			calls := new(atomic.Int32)
			payload := qPayload()
			uri := startQGoServer(t, secure, blockwise.SZX128, payload, new(qWireCounts), calls, config)
			cc := qDial(t, strings.TrimPrefix(strings.TrimPrefix(uri, "coaps://"), "coap://"),
				secure, blockwise.SZX128, options.QBlockwiseOnly, new(qWireCounts), config)
			response := qDo(t, cc, codes.GET, "/q", nil, message.Confirmable)
			body, err := response.ReadBody()
			require.NoError(t, err)
			require.Equal(t, payload, body)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestQBlockConfiguredSelectiveRecovery(t *testing.T) {
	payload := qPayload()
	for _, upload := range []bool{false, true} {
		t.Run(fmt.Sprintf("upload=%v", upload), func(t *testing.T) {
			config := qblock.DefaultConfig()
			config.MaxPayloads = 3
			config.NonTimeout, config.NonReceiveTimeout = 20*time.Millisecond, 1100*time.Millisecond
			config.NonMaxRetransmit, config.MaxLatency = 2, 10*time.Second
			calls := new(atomic.Int32)
			uri := startQGoServer(t, false, blockwise.SZX128, payload, new(qWireCounts), calls, config)
			option, method := message.QBlock2, codes.GET
			var body []byte
			if upload {
				option, method, body = message.QBlock1, codes.PUT, payload
			}

			addr, relay := newQDropRelay(t, strings.TrimPrefix(uri, "coap://"), option, 1, 4, 7)
			cc := qDial(t, addr, false, blockwise.SZX128, options.QBlockwiseOnly, new(qWireCounts), config)
			response := qDo(t, cc, method, "/q", body, message.NonConfirmable)
			got, err := response.ReadBody()
			require.NoError(t, err)
			require.Equal(t, payload, got)
			require.EqualValues(t, 1, calls.Load())
			relay.mu.Lock()
			defer relay.mu.Unlock()
			require.Empty(t, relay.drops)
			for num, count := range relay.observed {
				want := 1
				if num == 1 || num == 4 || num == 7 {
					want = 2
				}
				require.Equal(t, want, count, "NUM %d", num)
			}
		})
	}
}

func TestQBlockLibcoapConfiguredParameters(t *testing.T) {
	serverBinary := qLibcoapBinary(t, "coap-server")
	clientBinary := qLibcoapBinary(t, "coap-client")
	if qLibcoapMode(t) == qLibcoapDisabled {
		t.Log("Custom Q timers require Q-enabled libcoap; the fallback matrix covers the disabled reference.")
		return
	}
	addr := startQLibcoapServer(t, serverBinary, true)
	payload := qPayload()
	config := qblock.DefaultConfig()
	config.NonTimeout, config.NonReceiveTimeout = 100*time.Millisecond, 1200*time.Millisecond
	config.NonMaxRetransmit, config.MaxLatency = 2, 10*time.Second
	// libcoap uses MAX_PAYLOADS=10. Timers may differ; set boundaries must match.
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			counts := new(qWireCounts)
			cc := qDial(t, qSecureAddr(t, addr, secure), secure, blockwise.SZX128,
				options.QBlockwiseOnly, counts, config)
			path := fmt.Sprintf("/configured-%v", secure)
			for _, upload := range []bool{true, false} {
				method := codes.GET
				var body []byte
				if upload {
					method, body = codes.PUT, payload
				}
				response := qDo(t, cc, method, path, body, message.NonConfirmable)
				got, err := response.ReadBody()
				require.NoError(t, err)
				require.Equal(t, payload, got)
			}
			require.Positive(t, counts.q2.Load())
			require.Zero(t, counts.legacy.Load())

			serverCounts := new(qWireCounts)
			calls := new(atomic.Int32)
			uri := startQGoServer(t, secure, blockwise.SZX128, payload, serverCounts, calls, config)
			for _, body := range [][]byte{payload, nil} {
				got := runQLibcoapClient(t, clientBinary, uri+"/q", secure,
					message.NonConfirmable, blockwise.SZX128, body)
				require.Equal(t, payload, got)
			}
			require.EqualValues(t, 2, calls.Load())
			require.Positive(t, serverCounts.q1.Load())
			require.Positive(t, serverCounts.q2.Load())
			require.Zero(t, serverCounts.legacy.Load())
		})
	}
}
