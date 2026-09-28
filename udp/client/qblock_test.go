package client_test

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockDisabledRequestGate(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	var calls atomic.Int32
	server := udp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls.Add(1)
		_ = w.SetResponse(codes.Content, message.TextPlain, nil)
	}))
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()

	peer, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer peer.Close()

	cases := []struct {
		name         string
		typ          message.Type
		opts         message.Options
		wantResponse bool
	}{
		{"valid CON", message.Confirmable, message.Options{{ID: message.QBlock1}}, true},
		{"malformed CON", message.Confirmable, message.Options{{ID: message.QBlock1, Value: []byte{0, 0, 0, 0}}}, true},
		{"mixed CON", message.Confirmable, message.Options{{ID: message.QBlock1}, {ID: message.Block1}}, true},
		{"valid NON", message.NonConfirmable, message.Options{{ID: message.QBlock1}}, false},
		{"malformed NON", message.NonConfirmable, message.Options{{ID: message.QBlock2, Value: []byte{0, 0, 0, 0}}}, false},
		{"mixed NON", message.NonConfirmable, message.Options{{ID: message.Block2}, {ID: message.QBlock2}}, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := message.Message{Code: codes.GET, Type: tc.typ, MessageID: int32(i + 100), Token: []byte{byte(i + 1)}, Options: tc.opts}
			wire := make([]byte, 128)
			n, encodeErr := coder.DefaultCoder.Encode(request, wire)
			require.NoError(t, encodeErr)
			_, writeErr := peer.Write(wire[:n])
			require.NoError(t, writeErr)
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
			n, readErr := peer.Read(wire)
			if !tc.wantResponse {
				require.Error(t, readErr)
			} else {
				require.NoError(t, readErr)
				response := message.Message{Options: make(message.Options, 0, 4)}
				_, decodeErr := coder.DefaultCoder.Decode(wire[:n], &response)
				require.NoError(t, decodeErr)
				require.Equal(t, codes.BadOption, response.Code)
				require.Equal(t, request.Token, response.Token)
				if tc.typ == message.Confirmable {
					require.Equal(t, message.Acknowledgement, response.Type)
					require.Equal(t, request.MessageID, response.MessageID)
				} else {
					require.Equal(t, message.NonConfirmable, response.Type)
				}
			}
			require.Zero(t, calls.Load())
		})
	}

	request := message.Message{Code: codes.GET, Type: message.Confirmable, MessageID: 200, Token: []byte{0x7f}, Options: message.Options{{ID: message.RequestTag, Value: []byte{1}}}}
	wire := make([]byte, 128)
	n, err := coder.DefaultCoder.Encode(request, wire)
	require.NoError(t, err)
	_, err = peer.Write(wire[:n])
	require.NoError(t, err)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	n, err = peer.Read(wire)
	require.NoError(t, err)
	response := message.Message{Options: make(message.Options, 0, 4)}
	_, err = coder.DefaultCoder.Decode(wire[:n], &response)
	require.NoError(t, err)
	require.Equal(t, codes.Content, response.Code)
	require.EqualValues(t, 1, calls.Load())

	// A Q option on a response must not trigger a request-style Bad Option reply.
	unsolicited := message.Message{Code: codes.Content, Type: message.NonConfirmable, MessageID: 201, Token: []byte{0x7e}, Options: message.Options{{ID: message.QBlock2}}}
	n, err = coder.DefaultCoder.Encode(unsolicited, wire)
	require.NoError(t, err)
	_, err = peer.Write(wire[:n])
	require.NoError(t, err)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(200*time.Millisecond)))
	n, err = peer.Read(wire)
	if err == nil {
		response = message.Message{Options: make(message.Options, 0, 4)}
		_, err = coder.DefaultCoder.Decode(wire[:n], &response)
		require.NoError(t, err)
		require.NotEqual(t, codes.BadOption, response.Code)
	}
}

func TestQBlockMixedNONResponseCache(t *testing.T) {
	listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	var calls atomic.Int32
	server := udp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls.Add(1)
		_ = w.SetResponse(codes.Content, message.TextPlain, nil)
	}))
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	peer, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer peer.Close()

	exchange := func(req message.Message) message.Message {
		wire := make([]byte, 128)
		n, encodeErr := coder.DefaultCoder.Encode(req, wire)
		require.NoError(t, encodeErr)
		_, writeErr := peer.Write(wire[:n])
		require.NoError(t, writeErr)
		require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
		n, readErr := peer.Read(wire)
		require.NoError(t, readErr)
		resp := message.Message{Options: make(message.Options, 0, 4)}
		_, decodeErr := coder.DefaultCoder.Decode(wire[:n], &resp)
		require.NoError(t, decodeErr)
		return resp
	}

	mixed := message.Message{Code: codes.GET, Type: message.NonConfirmable, MessageID: 300, Token: []byte{1}, Options: message.Options{{ID: message.Block2}, {ID: message.QBlock2}}}
	first := exchange(mixed)
	require.Equal(t, codes.BadOption, first.Code)
	require.NotEqual(t, mixed.MessageID, first.MessageID)
	duplicate := exchange(mixed)
	require.Equal(t, first.MessageID, duplicate.MessageID)
	require.Equal(t, first.Token, duplicate.Token)
	require.Zero(t, calls.Load())

	ordinary := message.Message{Code: codes.GET, Type: message.NonConfirmable, MessageID: first.MessageID, Token: []byte{2}}
	response := exchange(ordinary)
	require.Equal(t, codes.Content, response.Code)
	require.Equal(t, ordinary.Token, response.Token)
	require.EqualValues(t, 1, calls.Load())
}
