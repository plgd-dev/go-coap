package dtls_test

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestQBlockServerCONProbeDTLS(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "piggyback", true: "separate"}[delayed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			sc := qblock.DefaultServerConfig()
			sc.ProbingRate = 65536
			var probes, gets, posts atomic.Int32
			s := dtls.NewServer(options.WithQBlockServer(sc), options.WithErrors(func(err error) { t.Logf("server error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
				path, _ := r.Path()
				if path == "/probe" {
					probes.Add(1)
					if delayed {
						select {
						case <-time.After(1200 * time.Millisecond):
						case <-r.Context().Done():
							return
						}
					}
				} else if r.Code() == codes.GET {
					gets.Add(1)
				} else {
					posts.Add(1)
					body, err := r.ReadBody()
					require.NoError(t, err)
					require.Equal(t, bytes.Repeat([]byte{'u'}, 40), body)
				}
				code := codes.Content
				if r.Code() != codes.GET {
					code = codes.Changed
				}
				require.NoError(t, w.SetResponse(code, message.TextPlain, bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz0123456789ABCD"))))
			}))
			l, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", qDTLSConfig())
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- s.Serve(l) }()
			defer func() { s.Stop(); require.NoError(t, <-done) }()
			cfg := qblock.DefaultClientConfig()
			cfg.Mode = qblock.Require
			cfg.ProbingRate = 65536
			cc, err := dtls.Dial(l.Addr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithQBlock(cfg), options.WithErrors(func(err error) { t.Logf("client error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second))
			require.NoError(t, err)
			defer cc.Close()
			supported, err := cc.ProbeQBlock(ctx, "/probe")
			require.NoError(t, err)
			require.True(t, supported)
			resp, err := cc.Get(ctx, "/get")
			require.NoError(t, err)
			payload, err := io.ReadAll(resp.Body())
			require.NoError(t, err)
			require.Equal(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCD", string(payload))
			cc.ReleaseMessage(resp)
			resp, err = cc.Post(ctx, "/upload", message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'u'}, 40)))
			require.NoError(t, err)
			require.Equal(t, codes.Changed, resp.Code())
			cc.ReleaseMessage(resp)
			require.EqualValues(t, 1, probes.Load())
			require.EqualValues(t, 1, gets.Load())
			require.EqualValues(t, 1, posts.Load())
		})
	}
}

func TestQBlockAcceptedOutboundDTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	q := qblock.DefaultClientConfig()
	q.Mode = qblock.Require
	q.ProbingRate = 65536
	workflow := make(chan struct {
		body     string
		postCode codes.Code
		err      error
	}, 1)
	addr, _ := startQDTLS(t, options.WithQBlock(q), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithOnNewConn(func(cc *client.Conn) {
		go func() {
			result := struct {
				body     string
				postCode codes.Code
				err      error
			}{}
			supported, err := cc.ProbeQBlock(ctx, "/probe")
			if err != nil {
				result.err = err
			} else if !supported {
				result.err = io.ErrUnexpectedEOF
			} else {
				resp, err := cc.Get(ctx, "/get")
				if err != nil {
					result.err = err
				} else {
					body, readErr := io.ReadAll(resp.Body())
					cc.ReleaseMessage(resp)
					if readErr != nil {
						result.err = readErr
					} else {
						result.body = string(body)
						resp, err = cc.Post(ctx, "/upload", message.TextPlain, bytes.NewReader([]byte("accepted upload")))
						if err != nil {
							result.err = err
						} else {
							result.postCode = resp.Code()
							cc.ReleaseMessage(resp)
						}
					}
				}
			}
			workflow <- result
		}()
	}), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		_ = w.SetResponse(codes.Changed, message.TextPlain, nil)
	}))
	peer := dialQDTLS(t, addr)
	require.NoError(t, peer.SetDeadline(time.Now().Add(8*time.Second)))
	trigger := pool.NewMessage(ctx)
	trigger.SetType(message.NonConfirmable)
	trigger.SetCode(codes.GET)
	trigger.SetMessageID(90)
	trigger.SetToken([]byte{90})
	require.NoError(t, trigger.SetPath("/trigger"))
	writeQDTLS(t, peer, trigger)

	var probes, gets, posts int
	for posts == 0 {
		req := readQDTLS(t, peer)
		if req.Code() != codes.GET && req.Code() != codes.POST {
			continue
		}
		path, err := req.Path()
		require.NoError(t, err)
		switch path {
		case "/probe":
			require.Equal(t, codes.GET, req.Code())
			require.Equal(t, message.Confirmable, req.Type())
			require.True(t, req.HasOption(message.QBlock2))
			probes++
			resp := pool.NewMessage(ctx)
			resp.SetType(message.Acknowledgement)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetOptionUint32(message.QBlock2, 0)
			resp.SetOptionUint32(message.Size2, 4)
			resp.SetOptionBytes(message.ETag, []byte{1})
			resp.SetBody(bytes.NewReader([]byte("peer")))
			writeQDTLS(t, peer, resp)
		case "/get":
			require.Equal(t, codes.GET, req.Code())
			require.True(t, req.HasOption(message.QBlock2))
			require.Equal(t, message.NonConfirmable, req.Type())
			gets++
			body := []byte("accepted reply")
			resp := pool.NewMessage(ctx)
			resp.SetType(message.NonConfirmable)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetOptionUint32(message.QBlock2, 0)
			resp.SetOptionUint32(message.Size2, uint32(len(body)))
			resp.SetOptionBytes(message.ETag, []byte{2})
			resp.SetBody(bytes.NewReader(body))
			writeQDTLS(t, peer, resp)
		case "/upload":
			require.Equal(t, codes.POST, req.Code())
			require.True(t, req.HasOption(message.QBlock1))
			require.Equal(t, message.NonConfirmable, req.Type())
			body, err := io.ReadAll(req.Body())
			require.NoError(t, err)
			require.Equal(t, "accepted upload", string(body))
			posts++
			resp := pool.NewMessage(ctx)
			resp.SetType(message.NonConfirmable)
			resp.SetCode(codes.Changed)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			writeQDTLS(t, peer, resp)
		}
	}
	result := <-workflow
	require.NoError(t, result.err)
	require.Equal(t, "accepted reply", result.body)
	require.Equal(t, codes.Changed, result.postCode)
	require.Equal(t, 1, probes)
	require.Equal(t, 1, gets)
	require.Equal(t, 1, posts)
}
