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

func TestDisabledQBlock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typ      message.Type
		rawOpts  []byte
		reply    bool
		ordinary bool
	}{
		{"CON q1", message.Confirmable, []byte{0xd0, 6}, true, false},
		{"CON q2", message.Confirmable, []byte{0xd0, 18}, true, false},
		{"CON malformed", message.Confirmable, []byte{0xd4, 6, 0, 0, 0, 0}, true, false},
		{"NON q1", message.NonConfirmable, []byte{0xd0, 6}, false, false},
		{"NON malformed", message.NonConfirmable, []byte{0xd4, 6, 0, 0, 0, 0}, false, false},
		{"CON mixed", message.Confirmable, []byte{0xd0, 6, 0x40}, true, false},
		{"NON mixed malformed Q", message.NonConfirmable, []byte{0xd4, 6, 0, 0, 0, 0, 0x40}, true, false},
		{"NON mixed malformed classic", message.NonConfirmable, []byte{0xd0, 6, 0x44, 0, 0, 0, 0}, true, false},
		{"NON mixed", message.NonConfirmable, []byte{0xd0, 6, 0x40}, true, false},
		{"tag only", message.Confirmable, []byte{0xe0, 0, 23}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			defer l.Close()
			var calls atomic.Int32
			s := udp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
				calls.Add(1)
				if e := w.SetResponse(codes.Content, message.TextPlain, nil); e != nil {
					t.Error(e)
				}
			}))
			done := make(chan error, 1)
			go func() { done <- s.Serve(l) }()
			defer func() { s.Stop(); require.NoError(t, <-done) }()
			c, err := net.Dial("udp4", l.LocalAddr().String())
			require.NoError(t, err)
			defer c.Close()
			require.NoError(t, c.SetDeadline(time.Now().Add(3*time.Second)))
			raw := append([]byte{0x41 | byte(tc.typ)<<4, byte(codes.GET), 0, 42, 7}, tc.rawOpts...)
			_, err = c.Write(raw)
			require.NoError(t, err)
			read := func() message.Message {
				buf := make([]byte, 1500)
				n, e := c.Read(buf)
				require.NoError(t, e)
				m := message.Message{Options: make(message.Options, 0, 16)}
				_, e = coder.DefaultCoder.Decode(buf[:n], &m)
				require.NoError(t, e)
				return m
			}
			if tc.reply {
				m := read()
				require.Equal(t, []byte{7}, []byte(m.Token))
				if tc.ordinary {
					require.Equal(t, codes.Content, m.Code)
				} else {
					require.Equal(t, codes.BadOption, m.Code)
				}
				if tc.typ == message.Confirmable {
					require.Equal(t, message.Acknowledgement, m.Type)
					require.Equal(t, int32(42), m.MessageID)
				} else {
					require.Equal(t, message.NonConfirmable, m.Type)
				}
			}
			// A later ordinary request provides a processing fence for silent NON cases.
			_, err = c.Write([]byte{0x41, 1, 0, 43, 8})
			require.NoError(t, err)
			m := read()
			require.Equal(t, []byte{8}, []byte(m.Token))
			require.Equal(t, codes.Content, m.Code)
			want := int32(1)
			if tc.ordinary {
				want++
			}
			require.Equal(t, want, calls.Load())
		})
	}
}

// A duplicate mixed NON request must replay the original error, including its
// fresh response MID, without invoking the application or changing its token.
func TestQBlockMixedNONResponseCache(t *testing.T) {
	l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	var calls atomic.Int32
	s := udp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls.Add(1)
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
	}))
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	defer func() { s.Stop(); require.NoError(t, <-done) }()
	peer, err := net.Dial("udp4", l.LocalAddr().String())
	require.NoError(t, err)
	defer peer.Close()
	require.NoError(t, peer.SetDeadline(time.Now().Add(3*time.Second)))

	exchange := func(req message.Message) message.Message {
		wire := make([]byte, 128)
		n, encodeErr := coder.DefaultCoder.Encode(req, wire)
		require.NoError(t, encodeErr)
		_, writeErr := peer.Write(wire[:n])
		require.NoError(t, writeErr)
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
