package client_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestObserveExpiredBlock2ResponseRestarts(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer server.Close()
	cc, err := udp.Dial(server.LocalAddr().String(), options.WithBlockwise(true, blockwise.SZX16, 50*time.Millisecond))
	require.NoError(t, err)
	defer cc.Close()
	first := bytes.Repeat([]byte{1}, 16)
	last := []byte{2, 3}
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- serveExpiredBlock2(server, cc.CheckExpirations, first, last)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		body []byte
		err  error
	}
	delivered := make(chan result, 1)
	_, err = cc.Observe(ctx, "/resource", func(r *pool.Message) {
		body, readErr := r.ReadBody()
		delivered <- result{body: body, err: readErr}
	})
	require.NoError(t, err)
	var got result
	select {
	case got = <-delivered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, got.err)
	require.Equal(t, append(first, last...), got.body)
	require.NoError(t, <-serverResult)
}

func serveExpiredBlock2(server net.PacketConn, expire func(time.Time), first, last []byte) error {
	if err := server.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return err
	}
	for i, expected := range []int64{0, 1, 0, 1} {
		buf := make([]byte, 2048)
		n, addr, err := server.ReadFrom(buf)
		if err != nil {
			return err
		}
		req := message.Message{Options: make(message.Options, 0, 16)}
		if _, err = coder.DefaultCoder.Decode(buf[:n], &req); err != nil {
			return err
		}
		if i != 0 {
			value, optionErr := req.Options.GetUint32(message.Block2)
			if optionErr != nil {
				return optionErr
			}
			_, num, _, decodeErr := blockwise.DecodeBlockOption(value)
			if decodeErr != nil {
				return decodeErr
			}
			if num != expected {
				return fmt.Errorf("unexpected requested block %d, expected %d", num, expected)
			}
		}
		if i == 1 {
			time.Sleep(100 * time.Millisecond)
			expire(time.Now())
		}
		value, err := blockwise.EncodeBlockOption(blockwise.SZX16, expected, expected == 0)
		if err != nil {
			return err
		}
		encoded := make([]byte, 4)
		size, err := message.EncodeUint32(encoded, value)
		if err != nil {
			return err
		}
		payload := first
		if expected != 0 {
			payload = last
		}
		response := message.Message{
			Code: codes.Content, Type: message.Acknowledgement, MessageID: req.MessageID, Token: req.Token,
			Options: message.Options{{ID: message.Block2, Value: encoded[:size]}}, Payload: payload,
		}
		n, err = coder.DefaultCoder.Encode(response, buf)
		if err != nil {
			return err
		}
		if _, err = server.WriteTo(buf[:n], addr); err != nil {
			return err
		}
	}
	return nil
}
