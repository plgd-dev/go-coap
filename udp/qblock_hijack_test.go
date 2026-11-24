package udp_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/stretchr/testify/require"
)

// A handler that hijacks a request owns it: the Q-Block engine must not
// return the reassembled request to the message pool after the handler returns.
func TestGoQBlockHijackedRequestIsNotReleased(t *testing.T) {
	payload := qPayload()
	type hijackedRequest struct {
		message *pool.Message
		release func()
	}
	hijacked := make(chan hijackedRequest, 2)
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/q", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		r.Hijack()
		conn := w.Conn()
		hijacked <- hijackedRequest{
			message: r.Message,
			release: func() { conn.ReleaseMessage(r.Message) },
		}
		if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(payload)); err != nil {
			t.Errorf("set Q response: %v", err)
		}
	})))
	l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	s := udp.NewServer(options.WithMux(router),
		options.WithBlockwise(true, blockwise.SZX128, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseAuto))
	served := make(chan error, 1)
	go func() { served <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Stop()
		require.NoError(t, l.Close())
		require.NoError(t, <-served)
	})
	cc := qDial(t, l.LocalAddr().String(), false, blockwise.SZX1024, options.QBlockwiseOnly, new(qWireCounts))

	for _, method := range []codes.Code{codes.PUT, codes.GET} {
		t.Run(method.String(), func(t *testing.T) {
			var body []byte
			if method == codes.PUT {
				body = payload
			}
			response := qDo(t, cc, method, "/q", body, message.Confirmable)
			require.Equal(t, codes.Content, response.Code())
			owned := <-hijacked
			defer owned.release()
			r := owned.message
			require.Equal(t, method, r.Code(), "hijacked request was returned to the pool")
			path, err := r.Path()
			require.NoError(t, err)
			require.Equal(t, "/q", path)
			if method == codes.PUT {
				got, err := r.ReadBody()
				require.NoError(t, err)
				require.Equal(t, payload, got)
			}
		})
	}
}
