package udp_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/require"
)

func TestQBlockSeededLoss(t *testing.T) {
	payload := qPayload()
	const szx = blockwise.SZX128
	count := (len(payload) + int(szx.Size()) - 1) / int(szx.Size())
	for _, percent := range []int{5, 10, 20} {
		for _, upload := range []bool{false, true} {
			t.Run(fmt.Sprintf("loss=%d/upload=%v", percent, upload), func(t *testing.T) {
				rng := rand.New(rand.NewPCG(uint64(percent), 9177))
				order := rng.Perm(count)
				n := max(1, count*percent/100)
				targets := make([]int64, n)
				for i := range targets {
					targets[i] = int64(order[i])
				}
				t.Logf("seed=(%d,9177), lost NUMs=%v", percent, targets)
				calls := new(atomic.Int32)
				server := startQGoServer(t, false, szx, payload, new(qWireCounts), calls)
				option, method := message.QBlock2, codes.GET
				var body []byte
				if upload {
					option, method, body = message.QBlock1, codes.PUT, payload
				}
				addr, relay := newQDropRelay(t, strings.TrimPrefix(server, "coap://"),
					option, targets[0], targets[1:]...)
				cc := qDial(t, addr, false, szx, options.QBlockwiseOnly, new(qWireCounts))
				resp := qDo(t, cc, method, "/q", body, message.NonConfirmable)
				got, err := resp.ReadBody()
				require.NoError(t, err)
				require.Equal(t, payload, got)
				require.Equal(t, int32(1), calls.Load())
				relay.mu.Lock()
				defer relay.mu.Unlock()
				require.Empty(t, relay.drops, "all selected losses must occur")
				for num := int64(0); num < int64(count); num++ {
					want := 1
					for _, lost := range targets {
						if num == lost {
							want = 2
						}
					}
					require.Equal(t, want, relay.observed[num], "unexpected retransmission count for NUM %d", num)
				}
			})
		}
	}
}
