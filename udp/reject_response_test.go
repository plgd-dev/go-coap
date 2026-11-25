package udp_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/dtls"
	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultDatagramHandlersRejectUnclaimedResponses(t *testing.T) {
	handlers := map[string]func(*responsewriter.ResponseWriter[*client.Conn], *pool.Message){
		"udp client":  client.DefaultConfig.Handler,
		"udp server":  server.DefaultConfig.Handler,
		"dtls client": dtls.DefaultConfig.Handler,
		"dtls server": dtlsServer.DefaultConfig.Handler,
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			request := pool.NewMessage(context.Background())
			request.SetType(message.Confirmable)
			request.SetCode(codes.Content)
			response := pool.NewMessage(context.Background())
			handler(responsewriter.New(response, (*client.Conn)(nil)), request)
			require.Equal(t, message.Reset, response.Type())

			request.SetCode(codes.GET)
			response = pool.NewMessage(context.Background())
			handler(responsewriter.New(response, (*client.Conn)(nil)), request)
			require.Equal(t, codes.NotFound, response.Code())
		})
	}
}

func TestUnrecognizedNotificationOnUDP(t *testing.T) {
	tests := []struct {
		name       string
		router     bool
		blockwise  bool
		block2     bool
		serverSide bool
	}{
		{name: "client default"},
		{name: "client router", router: true},
		{name: "client blockwise", blockwise: true, block2: true},
		{name: "client router blockwise", router: true, blockwise: true, block2: true},
		{name: "server default", serverSide: true},
		{name: "server router blockwise", serverSide: true, router: true, blockwise: true, block2: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			defer func() { require.NoError(t, peer.Close()) }()

			var target *net.UDPAddr
			if tt.serverSide {
				listener, listenErr := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
				require.NoError(t, listenErr)
				defer func() { require.NoError(t, listener.Close()) }()
				opts := []server.Option{options.WithBlockwise(tt.blockwise, blockwise.SZX1024, time.Minute)}
				if tt.router {
					opts = append(opts, options.WithHandlerFunc(mux.ToHandler[*client.Conn](mux.NewRouter())))
				}
				server := udp.NewServer(opts...)
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					assert.NoError(t, server.Serve(listener))
				}()
				defer func() { server.Stop(); wg.Wait() }()
				target, err = net.ResolveUDPAddr("udp4", listener.LocalAddr().String())
				require.NoError(t, err)
			} else {
				opts := []udp.Option{options.WithBlockwise(tt.blockwise, blockwise.SZX1024, time.Minute)}
				if tt.router {
					opts = append(opts, options.WithHandlerFunc(mux.ToHandler[*client.Conn](mux.NewRouter())))
				}
				cc, dialErr := udp.Dial(peer.LocalAddr().String(), opts...)
				require.NoError(t, dialErr)
				defer func() { require.NoError(t, cc.Close()); <-cc.Done() }()
				target, err = net.ResolveUDPAddr("udp4", cc.LocalAddr().String())
				require.NoError(t, err)
			}

			opts := []rawOption{{id: uint16(message.Observe), val: []byte{1}}}
			if tt.block2 {
				opts = append(opts, rawOption{id: uint16(message.Block2), val: []byte{8}})
			}
			packet := rawMessage(uint8(message.Confirmable), uint8(codes.Content), 0x1234, []byte{0xaa}, opts, []byte("notification"))
			_, err = peer.WriteToUDP(packet, target)
			require.NoError(t, err)
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
			buf := make([]byte, 1500)
			n, _, err := peer.ReadFromUDP(buf)
			require.NoError(t, err)
			require.Equal(t, []byte{0x70, 0x00, 0x12, 0x34}, buf[:n])
		})
	}
}
