package udp_test

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// TestServerBlockwiseLargeResponse is an end-to-end regression test for the Windows
// WriteMsgUDP n=0 bug. A response larger than a single block forces a Block2 transfer.
//
// Before the fix, on Windows the server saw a bogus ErrWriteInterrupted after sending the
// first block, aborted the blockwise session, and re-ran the handler for every follow-up
// block request, looping forever and leaking memory. The test would then hang until the
// context timeout.
//
// After the fix the transfer completes, the full payload is received, and the handler is
// invoked only a small, bounded number of times (no endless re-invocation).
func TestServerBlockwiseLargeResponse(t *testing.T) {
	const payloadSize = 10 * 1024
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	var handlerCalls atomic.Int32

	ld, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	sd := udp.NewServer(
		options.WithBlockwise(true, blockwise.SZX1024, time.Second*5),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handlerCalls.Inc()
			errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(payload))
			assert.NoError(t, errH)
		}),
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()
	defer func() {
		sd.Stop()
		wg.Wait()
	}()

	cc, err := udp.Dial(
		ld.LocalAddr().String(),
		options.WithBlockwise(true, blockwise.SZX1024, time.Second*5),
	)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()

	// The timeout guards against the endless loop: if the bug is present, Get never
	// completes and returns a deadline-exceeded error instead of hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	resp, err := cc.Get(ctx, "/test")
	require.NoError(t, err)
	require.Equal(t, codes.Content, resp.Code())

	body, err := io.ReadAll(resp.Body())
	require.NoError(t, err)
	require.Equal(t, payload, body)

	// The handler must be called once (allow a small margin for a possible retransmit),
	// not re-invoked for every block request as happened during the loop.
	calls := handlerCalls.Load()
	require.GreaterOrEqual(t, calls, int32(1))
	require.LessOrEqual(t, calls, int32(3))
}
