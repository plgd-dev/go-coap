package udp_test

import (
	"bytes"
	"context"
	"io"
	"net"
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

type rawOption struct {
	id  uint16
	val []byte
}

func rawUintValue(v uint32) []byte {
	switch {
	case v == 0:
		return nil
	case v < 256:
		return []byte{byte(v)}
	case v < 65536:
		return []byte{byte(v >> 8), byte(v)}
	default:
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

func rawBlockOption(num uint32, more bool, szx uint8) []byte {
	moreBit := uint32(0)
	if more {
		moreBit = 1
	}
	return rawUintValue((num << 4) | (moreBit << 3) | uint32(szx))
}

func encodeRawHeader(delta, length uint16) []byte {
	first := byte(0)
	var ext []byte
	switch {
	case delta < 13:
		first |= byte(delta << 4)
	case delta < 269:
		first |= 13 << 4
		ext = append(ext, byte(delta-13))
	default:
		first |= 14 << 4
		ext = append(ext, byte((delta-269)>>8), byte(delta-269))
	}
	switch {
	case length < 13:
		first |= byte(length)
	case length < 269:
		first |= 13
		ext = append(ext, byte(length-13))
	default:
		first |= 14
		ext = append(ext, byte((length-269)>>8), byte(length-269))
	}
	return append([]byte{first}, ext...)
}

func encodeRawOptions(opts []rawOption) []byte {
	var b bytes.Buffer
	prev := uint16(0)
	for _, opt := range opts {
		b.Write(encodeRawHeader(opt.id-prev, uint16(len(opt.val))))
		b.Write(opt.val)
		prev = opt.id
	}
	return b.Bytes()
}

func rawMessage(typ, code uint8, messageID uint16, token []byte, opts []rawOption, payload []byte) []byte {
	firstByte := byte(1<<6) | (typ << 4) | uint8(len(token))
	out := []byte{firstByte, code, byte(messageID >> 8), byte(messageID)}
	out = append(out, token...)
	out = append(out, encodeRawOptions(opts)...)
	if len(payload) > 0 {
		out = append(out, 0xFF)
		out = append(out, payload...)
	}
	return out
}

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

func TestServerBlockwiseMissingPreviousBlockReturnsRequestEntityIncomplete(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		errClose := listener.Close()
		require.NoError(t, errClose)
	}()

	var handlerCalls atomic.Int32
	server := udp.NewServer(
		options.WithBlockwise(true, blockwise.SZX1024, time.Second*5),
		options.WithBlockwiseReceivingMessagesCacheMaxEntries(1),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handlerCalls.Inc()
			errSet := w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("ok")))
			require.NoError(t, errSet)
		}),
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errServe := server.Serve(listener)
		assert.NoError(t, errServe)
	}()
	defer func() {
		server.Stop()
		wg.Wait()
	}()

	conn, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer func() {
		errClose := conn.Close()
		require.NoError(t, errClose)
	}()

	roundTrip := func(packet []byte, timeout time.Duration) []byte {
		_, errWrite := conn.Write(packet)
		require.NoError(t, errWrite)
		errDeadline := conn.SetReadDeadline(time.Now().Add(timeout))
		require.NoError(t, errDeadline)
		buf := make([]byte, 1500)
		n, errRead := conn.Read(buf)
		require.NoError(t, errRead)
		return buf[:n]
	}

	tests := []struct {
		name       string
		firstBlock bool
		num        uint32
		more       bool
	}{
		{name: "missing first non-final block", num: 1, more: true},
		{name: "missing first final block", num: 1},
		{name: "missing intermediate non-final block", firstBlock: true, num: 2, more: true},
		{name: "missing intermediate final block", firstBlock: true, num: 2},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := []byte{byte(i + 1)}
			mid := uint16(i*4 + 1)
			sendBlock := func(num uint32, more bool) []byte {
				packet := rawMessage(0, uint8(codes.PUT), mid, token, []rawOption{
					{id: uint16(message.URIPath), val: []byte("upload")},
					{id: uint16(message.Block1), val: rawBlockOption(num, more, 3)},
				}, bytes.Repeat([]byte("A"), 128))
				mid++
				response := roundTrip(packet, 2*time.Second)
				require.GreaterOrEqual(t, len(response), 2)
				return response
			}
			if tt.firstBlock {
				require.Equal(t, byte(codes.Continue), sendBlock(0, true)[1])
			}
			before := handlerCalls.Load()
			require.Equal(t, byte(codes.RequestEntityIncomplete), sendBlock(tt.num, tt.more)[1])
			require.Equal(t, before, handlerCalls.Load())

			// A rejected transfer must free its slot for a valid restart.
			require.Equal(t, byte(codes.Continue), sendBlock(0, true)[1])
			require.Equal(t, byte(codes.Changed), sendBlock(1, false)[1])
			require.Equal(t, before+1, handlerCalls.Load())
		})
	}
}
