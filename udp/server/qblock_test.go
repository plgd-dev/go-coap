package server_test

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/plgd-dev/go-coap/v3/udp/server"
	"github.com/stretchr/testify/require"
	"net"
	"testing"
	"time"
)

func TestQBlockServerPublicGET(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.ProbingRate = 4096
	srv := server.New(options.WithQBlockServer(cfg), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		require.Equal(t, codes.GET, r.Code())
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("public response"))))
	}))
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	defer func() { srv.Stop(); <-done }()
	socket, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer socket.Close()
	require.NoError(t, socket.SetDeadline(time.Now().Add(3*time.Second)))
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	req.SetToken([]byte{1})
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock2, 8)
	require.NoError(t, req.SetPath("/get"))
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = socket.Write(wire)
	require.NoError(t, err)
	buf := make([]byte, 2048)
	n, err := socket.Read(buf)
	require.NoError(t, err)
	resp := pool.NewMessage(context.Background())
	_, err = resp.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	require.Equal(t, codes.Content, resp.Code())
	require.True(t, resp.HasOption(message.QBlock2))
	body, err := resp.ReadBody()
	require.NoError(t, err)
	require.Equal(t, "public response", string(body))
}
func TestQBlockServerInvalidConfigBeforeServe(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxPeers = 0
	srv := server.New(options.WithQBlockServer(cfg))
	require.Error(t, srv.Serve(nil))
	srv.Stop()
}

func TestQBlockServerPublicQ1Upload(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.ProbingRate = 4096
	received := make(chan string, 1)
	srv := server.New(options.WithQBlockServer(cfg), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		body, err := r.ReadBody()
		require.NoError(t, err)
		received <- string(body)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("done"))))
	}))
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	defer func() { srv.Stop(); <-done }()
	socket, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer socket.Close()
	require.NoError(t, socket.SetDeadline(time.Now().Add(3*time.Second)))
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.POST)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	req.SetToken([]byte{1})
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock1, 0)
	req.SetOptionUint32(message.Size1, 4)
	req.SetBody(bytes.NewReader([]byte("body")))
	require.NoError(t, req.SetPath("/upload"))
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = socket.Write(wire)
	require.NoError(t, err)
	buf := make([]byte, 2048)
	n, err := socket.Read(buf)
	require.NoError(t, err)
	resp := pool.NewMessage(context.Background())
	_, err = resp.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	require.Equal(t, codes.Changed, resp.Code())
	require.True(t, resp.HasOption(message.QBlock2))
	select {
	case body := <-received:
		require.Equal(t, "body", body)
	case <-time.After(time.Second):
		t.Fatal("handler did not receive upload")
	}
}

func TestQBlockServerRejectsTruncatedOversizedUpload(t *testing.T) {
	req := pool.NewMessage(context.Background())
	req.SetCode(codes.POST)
	req.SetType(message.NonConfirmable)
	req.SetMessageID(1)
	req.SetToken([]byte{1})
	req.SetOptionBytes(message.RequestTag, []byte{1})
	req.SetOptionUint32(message.QBlock1, 0)
	req.SetOptionUint32(message.Size1, 4)
	req.SetBody(bytes.NewReader([]byte("body")))
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	calls := make(chan struct{}, 2)
	srv := server.New(options.WithQBlockServer(qblock.DefaultServerConfig()), options.WithMaxMessageSize(uint32(len(wire))), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls <- struct{}{}
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	}))
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	defer func() { srv.Stop(); <-done }()
	socket, err := net.DialUDP("udp4", nil, listener.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer socket.Close()
	_, err = socket.Write(append(append([]byte(nil), wire...), []byte("extra")...))
	require.NoError(t, err)
	select {
	case <-calls:
		t.Fatal("oversized truncated datagram dispatched upload")
	case <-time.After(100 * time.Millisecond):
	}
	_, err = socket.Write(wire)
	require.NoError(t, err)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("valid retry not dispatched")
	}
}
