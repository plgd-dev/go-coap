package udp_test

import (
	"bytes"
	"context"
	"errors"
	"net"
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
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestGoQBlockObserveReset(t *testing.T) {
	for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable} {
		t.Run(typ.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			notify := make(chan func() error, 1)
			router := mux.NewRouter()
			require.NoError(t, router.Handle("/observe", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
				if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader([]byte("initial"))); err != nil {
					t.Error(err)
					return
				}
				w.Message().SetObserve(10)
				cc, token := w.Conn(), bytes.Clone(r.Token())
				notify <- func() error {
					response := cc.AcquireMessage(ctx)
					defer cc.ReleaseMessage(response)
					response.SetCode(codes.Content)
					response.SetType(typ)
					response.SetToken(token)
					response.SetObserve(11)
					response.SetBody(bytes.NewReader(bytes.Repeat([]byte{7}, 2048)))
					return cc.WriteMessage(response)
				}
			})))
			listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			server := udp.NewServer(options.WithMux(router), options.WithQBlockwise(options.QBlockwiseAuto),
				options.WithBlockwise(true, blockwise.SZX128, time.Second))
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			defer func() { server.Stop(); require.NoError(t, listener.Close()); require.NoError(t, <-done) }()
			serverAddr, ok := listener.LocalAddr().(*net.UDPAddr)
			require.True(t, ok)
			peer, err := net.DialUDP("udp4", nil, serverAddr)
			require.NoError(t, err)
			defer func() { require.NoError(t, peer.Close()) }()
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(4*time.Second)))
			send := func(msg *pool.Message) {
				t.Helper()
				wire, encodeErr := msg.MarshalWithEncoder(coder.DefaultCoder)
				require.NoError(t, encodeErr)
				_, writeErr := peer.Write(wire)
				require.NoError(t, writeErr)
			}
			receive := func() *pool.Message {
				t.Helper()
				wire := make([]byte, 4096)
				n, readErr := peer.Read(wire)
				require.NoError(t, readErr)
				msg := pool.NewMessage(ctx)
				_, decodeErr := msg.UnmarshalWithDecoder(coder.DefaultCoder, wire[:n])
				require.NoError(t, decodeErr)
				return msg
			}
			request := pool.NewMessage(ctx)
			request.SetCode(codes.GET)
			request.SetType(message.NonConfirmable)
			request.SetMessageID(100)
			request.SetToken([]byte("reactive"))
			request.SetObserve(0)
			require.NoError(t, request.SetPath("/observe"))
			value, err := blockwise.EncodeQBlockOption(blockwise.SZX128, 0, true)
			require.NoError(t, err)
			request.SetOptionUint32(message.QBlock2, value)
			send(request)
			initial := receive()
			require.True(t, initial.HasOption(message.Observe))
			require.True(t, initial.HasOption(message.QBlock2))
			write := <-notify
			written := make(chan error, 1)
			go func() { written <- write() }()
			notification := receive()
			reset := pool.NewMessage(ctx)
			reset.SetCode(codes.Empty)
			reset.SetType(message.Reset)
			reset.SetMessageID(notification.MessageID())
			send(reset)
			err = <-written
			if typ == message.Confirmable {
				require.Error(t, err, "a Reset must not be reported as a successful notification ACK")
			} else {
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool {
				return errors.Is(write(), context.Canceled)
			}, time.Second, time.Millisecond, "a rejected Q observer must not silently become a legacy observer")
		})
	}
}
