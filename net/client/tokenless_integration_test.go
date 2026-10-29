package client_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/client"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tokenlessTestConn interface {
	NewGetRequest(context.Context, string, ...message.Option) (*pool.Message, error)
	Do(*pool.Message) (*pool.Message, error)
	ReleaseMessage(*pool.Message)
	Close() error
}

const (
	tokenlessTestUDP = "udp"
	tokenlessTestTCP = "tcp"
)

type unreadableBody struct {
	*bytes.Reader
}

func (unreadableBody) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestTokenlessMarshalFailureAllowsRetry(t *testing.T) {
	for _, transport := range []string{tokenlessTestUDP, tokenlessTestTCP} {
		t.Run(transport, func(t *testing.T) {
			router := mux.NewRouter()
			require.NoError(t, router.Handle("/valid", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
				assert.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
			})))
			cc := startTokenlessTestServer(t, transport, router)()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			invalid, err := cc.NewGetRequest(ctx, "/valid")
			require.NoError(t, err)
			defer cc.ReleaseMessage(invalid)
			invalid.SetToken(nil)
			invalid.SetBody(unreadableBody{Reader: bytes.NewReader([]byte("payload"))})
			_, err = cc.Do(invalid)
			require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			require.NotErrorIs(t, err, client.ErrTokenlessRequestsBlocked)

			valid, err := cc.NewGetRequest(ctx, "/valid")
			require.NoError(t, err)
			defer cc.ReleaseMessage(valid)
			valid.SetToken(nil)
			resp, err := cc.Do(valid)
			require.NoError(t, err)
			defer cc.ReleaseMessage(resp)
			require.Equal(t, codes.Content, resp.Code())
		})
	}
}

func TestTokenlessTimeoutBlocksConnection(t *testing.T) {
	for _, transport := range []string{tokenlessTestUDP, tokenlessTestTCP} {
		t.Run(transport, func(t *testing.T) {
			received := make(chan struct{}, 1)
			sendLate := make(chan struct{})
			lateSent := make(chan struct{})
			router := mux.NewRouter()
			require.NoError(t, router.Handle("/slow", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
				received <- struct{}{}
				<-sendLate
				assert.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
				close(lateSent)
			})))
			require.NoError(t, router.Handle("/fast", mux.HandlerFunc(func(w mux.ResponseWriter, _ *mux.Message) {
				assert.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
			})))
			dial := startTokenlessTestServer(t, transport, router)
			// Release a pending server handler even when an assertion fails.
			defer func() {
				select {
				case <-sendLate:
				default:
					close(sendLate)
				}
			}()
			cc := dial()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := cc.NewGetRequest(ctx, "/slow")
			require.NoError(t, err)
			req.SetToken(nil)
			requestCtx, cancelRequest := context.WithCancel(ctx)
			req.SetContext(requestCtx)
			result := make(chan error, 1)
			go func() {
				_, requestErr := cc.Do(req)
				result <- requestErr
			}()
			select {
			case <-received:
			case <-ctx.Done():
				t.Fatal("server did not receive tokenless request")
			}
			cancelRequest()
			select {
			case err = <-result:
			case <-ctx.Done():
				t.Fatal("canceled tokenless request did not return")
			}
			cc.ReleaseMessage(req)
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, err, client.ErrTokenlessRequestsBlocked)
			require.Contains(t, err.Error(), "recreate the connection")

			checkGet := func(connection tokenlessTestConn, tokenless bool, wantBlocked bool) {
				t.Helper()
				get, getErr := connection.NewGetRequest(ctx, "/fast")
				require.NoError(t, getErr)
				defer connection.ReleaseMessage(get)
				if tokenless {
					get.SetToken(nil)
				}
				resp, doErr := connection.Do(get)
				if wantBlocked {
					require.ErrorIs(t, doErr, client.ErrTokenlessRequestsBlocked)
					return
				}
				require.NoError(t, doErr)
				defer connection.ReleaseMessage(resp)
				require.Equal(t, codes.Content, resp.Code())
			}
			checkGet(cc, true, true)
			close(sendLate)
			select {
			case <-lateSent:
			case <-ctx.Done():
				t.Fatal("server did not produce late response")
			}
			checkGet(cc, false, false)
			checkGet(cc, true, true)
			checkGet(dial(), true, false)
		})
	}
}

func startTokenlessTestServer(t *testing.T, transport string, router *mux.Router) func() tokenlessTestConn {
	t.Helper()
	var address string
	done := make(chan error, 1)
	switch transport {
	case tokenlessTestUDP:
		listener, err := coapNet.NewListenUDP("udp", "127.0.0.1:0")
		require.NoError(t, err)
		server := udp.NewServer(options.WithMux(router))
		address = listener.LocalAddr().String()
		go func() { done <- server.Serve(listener) }()
		t.Cleanup(func() {
			server.Stop()
			assert.NoError(t, <-done)
			assert.NoError(t, listener.Close())
		})
	case tokenlessTestTCP:
		listener, err := coapNet.NewTCPListener("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		server := tcp.NewServer(options.WithMux(router))
		address = listener.Addr().String()
		go func() { done <- server.Serve(listener) }()
		t.Cleanup(func() {
			server.Stop()
			assert.NoError(t, <-done)
			assert.NoError(t, listener.Close())
		})
	}
	return func() tokenlessTestConn {
		var connection tokenlessTestConn
		var err error
		if transport == tokenlessTestUDP {
			connection, err = udp.Dial(address, options.WithBlockwise(false, blockwise.SZX1024, time.Second))
		} else {
			connection, err = tcp.Dial(address, options.WithBlockwise(false, blockwise.SZX1024, time.Second))
		}
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, connection.Close()) })
		return connection
	}
}
