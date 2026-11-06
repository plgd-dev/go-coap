package udp_test

import (
	"bytes"
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

func TestQBlockMissingACKDoesNotBlockOtherBody(t *testing.T) {
	payload := make([]byte, 12*16)
	uri := startQGoServer(t, false, blockwise.SZX16, payload, new(qWireCounts), new(atomic.Int32))
	addr, err := net.ResolveUDPAddr("udp4", strings.TrimPrefix(uri, "coap://"))
	require.NoError(t, err)
	conn, err := net.DialUDP("udp4", nil, addr)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	req := message.Message{
		Code: codes.GET, Type: message.Confirmable, MessageID: 1, Token: message.Token("blocked"),
		Options: message.Options{
			{ID: message.URIPath, Value: []byte("q")},
			{ID: message.QBlock2, Value: []byte{8}},
		},
	}
	qRawSend(t, conn, req)
	read := func() message.Message {
		data := make([]byte, 2048)
		n, readErr := conn.Read(data)
		require.NoError(t, readErr)
		resp := message.Message{Options: make(message.Options, 0, 16)}
		_, decodeErr := coder.DefaultCoder.Decode(data[:n], &resp)
		require.NoError(t, decodeErr)
		return resp
	}
	first := read()
	require.Equal(t, message.Acknowledgement, first.Type)
	require.Equal(t, int32(1), first.MessageID)
	pending := read()
	require.Equal(t, message.Confirmable, pending.Type)
	require.Equal(t, message.Token("blocked"), pending.Token)
	// Deliberately do not ACK the CON body. Its first retransmission is not due
	// until at least two seconds; the other body's scheduler must run now.
	req.Type, req.MessageID, req.Token = message.NonConfirmable, 2, message.Token("free")
	qRawSend(t, conn, req)
	received := make(map[int64]bool)
	for len(received) < 10 {
		resp := read()
		if !bytes.Equal(resp.Token, req.Token) {
			continue
		}
		value, optionErr := resp.Options.GetUint32(message.QBlock2)
		require.NoError(t, optionErr)
		_, num, _, decodeErr := blockwise.DecodeQBlockOption(value)
		require.NoError(t, decodeErr)
		received[num] = true
	}
	require.Len(t, received, 10)
}
