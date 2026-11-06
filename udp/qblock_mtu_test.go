package udp_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockServerConfiguredMTU(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			testQBlockServerConfiguredMTU(t, secure)
		})
	}
}

func testQBlockServerConfiguredMTU(t *testing.T, secure bool) {
	const mtu = 256
	limit := mtu
	if secure {
		limit -= 128
	}
	payload := qPayload()
	uri := startQGoServerWithMTU(t, secure, blockwise.SZX1024, payload,
		new(qWireCounts), new(atomic.Int32), qGoServerOptions{mtu: mtu})
	addr := strings.TrimPrefix(strings.TrimPrefix(uri, "coaps://"), "coap://")
	var largest atomic.Int64
	monitor := func(_ *client.Conn, msg *pool.Message) (bool, error) {
		return measureQContentWire(msg, &largest)
	}
	opts := []udp.Option{
		options.WithQBlockwise(options.QBlockwiseOnly),
		options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
		options.WithRequestMonitor(monitor),
	}
	cc := qDialWithOptions(t, addr, secure, opts...)
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := cc.Get(ctx, "/q")
	require.NoError(t, err)
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.Equal(t, payload, body)
	require.Positive(t, largest.Load())
	require.LessOrEqual(t, largest.Load(), int64(limit))
}

func measureQContentWire(msg *pool.Message, largest *atomic.Int64) (bool, error) {
	if msg.Code() != codes.Content || !msg.HasOption(message.QBlock2) {
		return false, nil
	}
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return false, err
	}
	for previous := largest.Load(); int64(len(wire)) > previous; previous = largest.Load() {
		if largest.CompareAndSwap(previous, int64(len(wire))) {
			break
		}
	}
	return false, nil
}

func qDialWithOptions(t *testing.T, addr string, secure bool, opts ...udp.Option) *client.Conn {
	t.Helper()
	var cc *client.Conn
	var err error
	if secure {
		cc, err = dtls.Dial(addr, dtls.NewDTLSClientOptions(
			piondtls.WithPSK(qLibcoapPSKCallback),
			piondtls.WithPSKIdentityHint([]byte("qblock-test")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256)), opts...)
	} else {
		cc, err = udp.Dial(addr, opts...)
	}
	require.NoError(t, err)
	return cc
}
