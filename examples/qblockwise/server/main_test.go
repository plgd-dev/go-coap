package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/stretchr/testify/require"
)

func TestQBlockwiseExample(t *testing.T) {
	router, err := newRouter()
	require.NoError(t, err)
	listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	server := udp.NewServer(
		options.WithMux(router),
		options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
	)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		require.NoError(t, <-done)
	})
	for _, tc := range []struct {
		name string
		mode options.QBlockwiseMode
	}{
		{"QBlockwiseAuto", options.QBlockwiseAuto},
		{"QBlockwiseOnly", options.QBlockwiseOnly},
		{"QBlockwiseDisabled", options.QBlockwiseDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cc, dialErr := udp.Dial(listener.LocalAddr().String(),
				options.WithQBlockwise(tc.mode),
				options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
			)
			require.NoError(t, dialErr)
			defer func() { require.NoError(t, cc.Close()) }()

			get, createErr := cc.NewGetRequest(ctx, "/data")
			require.NoError(t, createErr)
			defer cc.ReleaseMessage(get)
			get.SetType(message.NonConfirmable)
			download, doErr := cc.Do(get)
			require.NoError(t, doErr)
			defer cc.ReleaseMessage(download)
			require.Equal(t, codes.Content, download.Code())
			body, readErr := download.ReadBody()
			require.NoError(t, readErr)
			require.Equal(t, bytes.Repeat([]byte("0123456789abcdef"), 4096), body)

			upload := bytes.Repeat([]byte{0, 0xff, 3}, 4097)
			put, createErr := cc.NewPutRequest(ctx, "/data", message.AppOctets, bytes.NewReader(upload))
			require.NoError(t, createErr)
			defer cc.ReleaseMessage(put)
			put.SetType(message.NonConfirmable)
			echo, doErr := cc.Do(put)
			require.NoError(t, doErr)
			defer cc.ReleaseMessage(echo)
			require.Equal(t, codes.Changed, echo.Code())
			body, readErr = echo.ReadBody()
			require.NoError(t, readErr)
			require.Equal(t, upload, body)

			notAllowed, postErr := cc.Post(ctx, "/data", message.AppOctets, bytes.NewReader([]byte{1}))
			require.NoError(t, postErr)
			defer cc.ReleaseMessage(notAllowed)
			require.Equal(t, codes.MethodNotAllowed, notAllowed.Code())
		})
	}
}
