package udp_test

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestQBlockServerCONProbeUDP(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(map[bool]string{false: "piggyback", true: "separate"}[delayed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			sc := qblock.DefaultServerConfig()
			sc.ProbingRate = 65536
			var probes, gets, posts atomic.Int32
			s := udp.NewServer(options.WithQBlockServer(sc), options.WithErrors(func(err error) { t.Logf("server error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
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
			l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- s.Serve(l) }()
			defer func() { s.Stop(); require.NoError(t, <-done) }()
			cfg := qblock.DefaultClientConfig()
			cfg.Mode = qblock.Require
			cfg.ProbingRate = 65536
			cc, err := udp.Dial(l.LocalAddr().String(), options.WithContext(ctx), options.WithQBlock(cfg), options.WithErrors(func(err error) { t.Logf("client error: %v", err) }), options.WithBlockwise(false, blockwise.SZX16, time.Second))
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

func TestQBlockAcceptedOutboundUDP(t *testing.T) {
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
	s := udp.NewServer(options.WithQBlock(q), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithOnNewConn(func(cc *client.Conn) {
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
	l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	defer func() { s.Stop(); require.NoError(t, <-done) }()
	peer, err := net.DialUDP("udp4", nil, l.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer peer.Close()
	require.NoError(t, peer.SetDeadline(time.Now().Add(8*time.Second)))
	trigger := pool.NewMessage(ctx)
	trigger.SetType(message.NonConfirmable)
	trigger.SetCode(codes.GET)
	trigger.SetMessageID(90)
	trigger.SetToken([]byte{90})
	require.NoError(t, trigger.SetPath("/trigger"))
	wire, err := trigger.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.Write(wire)
	require.NoError(t, err)

	var probes, gets, posts int
	for posts == 0 {
		buf := make([]byte, 2048)
		n, readErr := peer.Read(buf)
		require.NoError(t, readErr)
		req := pool.NewMessage(ctx)
		_, readErr = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
		require.NoError(t, readErr)
		if req.Code() != codes.GET && req.Code() != codes.POST {
			continue
		}
		path, pathErr := req.Path()
		require.NoError(t, pathErr)
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
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
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
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
		case "/upload":
			require.Equal(t, codes.POST, req.Code())
			require.True(t, req.HasOption(message.QBlock1))
			require.Equal(t, message.NonConfirmable, req.Type())
			require.Equal(t, "accepted upload", string(mustReadQBlockBody(t, req)))
			posts++
			resp := pool.NewMessage(ctx)
			resp.SetType(message.NonConfirmable)
			resp.SetCode(codes.Changed)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
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

func mustReadQBlockBody(t *testing.T, msg *pool.Message) []byte {
	t.Helper()
	body, err := io.ReadAll(msg.Body())
	require.NoError(t, err)
	return body
}
