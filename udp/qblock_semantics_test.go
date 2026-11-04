package udp_test

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func qRawResponse(t *testing.T, addr string, req message.Message) message.Message {
	t.Helper()
	target, err := net.ResolveUDPAddr("udp4", addr)
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, target)
	require.NoError(t, err)
	defer conn.Close()
	return qRawRoundTrip(t, conn, req)
}

func qRawRoundTrip(t *testing.T, conn *net.UDPConn, req message.Message) message.Message {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	qRawSend(t, conn, req)
	for {
		data := make([]byte, 65535)
		n, err := conn.Read(data)
		require.NoError(t, err)
		resp := message.Message{Options: make(message.Options, 0, 32)}
		_, err = coder.DefaultCoder.Decode(data[:n], &resp)
		require.NoError(t, err)
		if resp.Code != codes.Empty {
			return resp
		}
	}
}

func qRawSend(t *testing.T, conn *net.UDPConn, req message.Message) {
	t.Helper()
	size, err := coder.DefaultCoder.Size(req)
	require.NoError(t, err)
	buf := make([]byte, size)
	_, err = coder.DefaultCoder.Encode(req, buf)
	require.NoError(t, err)
	_, err = conn.Write(buf)
	require.NoError(t, err)
}

func TestQBlockOutOfOrderRequest(t *testing.T) {
	payload := make([]byte, 37)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	calls := new(atomic.Int32)
	uri := startQGoServer(t, false, blockwise.SZX1024, payload, new(qWireCounts), calls)
	addr, err := net.ResolveUDPAddr("udp4", strings.TrimPrefix(uri, "coap://"))
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, addr)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	blocks := []struct {
		control byte
		payload []byte
	}{
		{0x20, payload[32:]},
		{0x18, payload[16:32]},
		{0x18, make([]byte, 16)},
		{0x08, payload[:16]},
	}
	for i, block := range blocks {
		req := message.Message{
			Code: codes.PUT, Type: message.NonConfirmable, MessageID: int32(i + 1),
			Token: message.Token{1, byte(i + 1)},
			Options: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{block.control}},
				{ID: message.QBlock2, Value: []byte{0x0e}},
				{ID: message.Size1, Value: []byte{37}},
				{ID: message.RequestTag, Value: []byte{9}},
			},
			Payload: block.payload,
		}
		if i < len(blocks)-1 {
			qRawSend(t, conn, req)
			continue
		}
		resp := qRawRoundTrip(t, conn, req)
		require.Equal(t, codes.Changed, resp.Code)
		require.Equal(t, payload, resp.Payload, "duplicate data must not replace the first payload")
		require.Equal(t, req.Token, resp.Token)
		require.Equal(t, int32(1), calls.Load())
	}
}

func TestQBlockDuplicateCompletedRequest(t *testing.T) {
	payload := []byte{1, 2, 3}
	calls := new(atomic.Int32)
	uri := startQGoServer(t, false, blockwise.SZX16, payload, new(qWireCounts), calls)
	addr, err := net.ResolveUDPAddr("udp4", strings.TrimPrefix(uri, "coap://"))
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, addr)
	require.NoError(t, err)
	defer conn.Close()
	req := message.Message{
		Code: codes.PUT, Type: message.Confirmable, MessageID: 1,
		Token: message.Token{1, 2, 3},
		Options: message.Options{
			{ID: message.URIPath, Value: []byte("q")},
			{ID: message.QBlock1, Value: []byte{}},
			{ID: message.Size1, Value: []byte{3}},
			{ID: message.RequestTag, Value: []byte{9}},
		},
		Payload: payload,
	}
	first := qRawRoundTrip(t, conn, req)
	require.Equal(t, codes.Changed, first.Code)
	require.Equal(t, int32(1), calls.Load())
	req.MessageID = 2
	req.Token = message.Token{4, 5, 6}
	second := qRawRoundTrip(t, conn, req)
	require.Equal(t, codes.Changed, second.Code)
	require.Equal(t, req.Token, second.Token, "repeat responds to its own request token")
	require.Equal(t, payload, second.Payload)
	require.Equal(t, int32(1), calls.Load(), "duplicate completed body must not invoke application again")
}

func TestQBlockEmptyRequestTag(t *testing.T) {
	payload := []byte{42}
	calls := new(atomic.Int32)
	uri := startQGoServer(t, false, blockwise.SZX16, payload, new(qWireCounts), calls)
	resp := qRawResponse(t, strings.TrimPrefix(uri, "coap://"), message.Message{
		Code: codes.PUT, Type: message.Confirmable, MessageID: 1,
		Token: message.Token{1},
		Options: message.Options{
			{ID: message.URIPath, Value: []byte("q")},
			{ID: message.QBlock1, Value: []byte{}},
			{ID: message.Size1, Value: []byte{1}},
			{ID: message.RequestTag, Value: []byte{}},
		},
		Payload: payload,
	})
	require.Equal(t, codes.Changed, resp.Code)
	require.Equal(t, payload, resp.Payload)
	require.Equal(t, int32(1), calls.Load())
}

func TestQBlockInvalidWireRequests(t *testing.T) {
	tests := []struct {
		name string
		opts message.Options
		code codes.Code
	}{
		{
			name: "missing-request-tag",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{}},
				{ID: message.Size1, Value: []byte{1}},
			},
			code: codes.BadRequest,
		},
		{
			name: "missing-size",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{}},
				{ID: message.RequestTag, Value: []byte{1}},
			},
			code: codes.BadRequest,
		},
		{
			name: "mixed-option-families",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{}},
				{ID: message.Block2, Value: []byte{}},
				{ID: message.Size1, Value: []byte{1}},
				{ID: message.RequestTag, Value: []byte{1}},
			},
			code: codes.BadOption,
		},
		{
			name: "duplicate-qblock1",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{}},
				{ID: message.QBlock1, Value: []byte{}},
				{ID: message.Size1, Value: []byte{1}},
				{ID: message.RequestTag, Value: []byte{1}},
			},
			code: codes.BadOption,
		},
		{
			name: "bert-on-datagram",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock1, Value: []byte{7}},
				{ID: message.Size1, Value: []byte{1}},
				{ID: message.RequestTag, Value: []byte{1}},
			},
			code: codes.BadOption,
		},
		{
			name: "descending-qblock2-recovery",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock2, Value: []byte{0x36}},
				{ID: message.QBlock2, Value: []byte{0x16}},
			},
			code: codes.BadRequest,
		},
		{
			name: "duplicate-qblock2-recovery",
			opts: message.Options{
				{ID: message.URIPath, Value: []byte("q")},
				{ID: message.QBlock2, Value: []byte{0x16}},
				{ID: message.QBlock2, Value: []byte{0x16}},
			},
			code: codes.BadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := new(atomic.Int32)
			uri := startQGoServer(t, false, blockwise.SZX128, qPayload(), new(qWireCounts), calls)
			resp := qRawResponse(t, strings.TrimPrefix(uri, "coap://"), message.Message{
				Code: codes.PUT, Type: message.Confirmable, MessageID: 1,
				Token: message.Token{1, 2, 3}, Options: tt.opts, Payload: []byte{42},
			})
			require.Equal(t, tt.code, resp.Code)
			require.Equal(t, message.Token{1, 2, 3}, resp.Token)
			require.Zero(t, calls.Load(), "invalid requests must never reach the application")
		})
	}
}
