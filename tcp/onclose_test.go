package tcp_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/plgd-dev/go-coap/v3/tcp/coder"
	"github.com/stretchr/testify/require"
)

func TestHandlerAddOnCloseAfterPeerDisconnect(t *testing.T) {
	listener, err := coapNet.NewTCPListener("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	started := make(chan struct{})
	cleaned := make(chan struct{})
	server := tcp.NewServer(options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		close(started)
		<-w.Conn().Done()
		w.Conn().AddOnClose(func() { close(cleaned) })
	}))
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		require.NoError(t, <-serverDone)
	}()

	conn, err := net.Dial("tcp4", listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	require.NoError(t, req.SetPath("/resource"))
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = conn.Write(data)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request handler did not start")
	}
	require.NoError(t, conn.Close())
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("close callback was not called")
	}
}
