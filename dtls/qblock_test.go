package dtls_test

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockCapabilityProbeDTLS(t *testing.T) {
	for _, classic := range []bool{false, true} {
		t.Run(map[bool]string{false: "classic_disabled", true: "classic_enabled"}[classic], func(t *testing.T) {
			l, err := piondtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, qDTLSConfig())
			require.NoError(t, err)
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cc, err := dtls.Dial(l.Addr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithBlockwise(classic, blockwise.SZX16, time.Second))
			require.NoError(t, err)
			defer cc.Close()
			result := make(chan error, 1)
			go func() {
				ok, err := cc.ProbeQBlock(ctx, "")
				if err == nil && !ok {
					err = net.ErrClosed
				}
				result <- err
			}()
			peer, err := l.Accept()
			require.NoError(t, err)
			defer peer.Close()
			require.NoError(t, peer.SetDeadline(time.Now().Add(3*time.Second)))
			buf := make([]byte, 2048)
			n, err := peer.Read(buf)
			require.NoError(t, err)
			req := pool.NewMessage(ctx)
			_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
			require.NoError(t, err)
			require.Equal(t, message.Confirmable, req.Type())
			require.Equal(t, codes.GET, req.Code())
			require.Nil(t, req.Body())
			path, err := req.Path()
			require.NoError(t, err)
			require.Equal(t, "/.well-known/core", path)
			value, err := req.GetOptionUint32(message.QBlock2)
			require.NoError(t, err)
			require.Zero(t, value)
			require.Len(t, req.Options(), 3)
			resp := pool.NewMessage(ctx)
			resp.SetType(message.Acknowledgement)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetOptionUint32(message.QBlock2, 8)
			resp.SetOptionBytes(message.ETag, []byte{1})
			resp.SetOptionUint32(message.Size2, 1048576)
			resp.SetBody(bytes.NewReader([]byte("0123456789abcdef")))
			wire, err := resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
			require.NoError(t, <-result)
			go func() {
				r, err := cc.Get(ctx, "/ordinary")
				if r != nil {
					cc.ReleaseMessage(r)
				}
				result <- err
			}()
			n, err = peer.Read(buf)
			require.NoError(t, err)
			req = pool.NewMessage(ctx)
			_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
			require.NoError(t, err)
			path, err = req.Path()
			require.NoError(t, err)
			require.Equal(t, "/ordinary", path)
			require.False(t, req.HasOption(message.QBlock2))
			resp = pool.NewMessage(ctx)
			resp.SetType(message.Acknowledgement)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetBody(bytes.NewReader([]byte("ordinary")))
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
			require.NoError(t, <-result)
		})
	}
}

func qDTLSConfig() *piondtls.Config {
	return &piondtls.Config{PSK: func([]byte) ([]byte, error) { return []byte{1, 2, 3, 4}, nil }, PSKIdentityHint: []byte("qblock"), CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8}}
}

func startQDTLS(t *testing.T, opts ...server.Option) (string, *server.Server) {
	t.Helper()
	l, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", qDTLSConfig())
	require.NoError(t, err)
	s := dtls.NewServer(opts...)
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() { s.Stop(); require.NoError(t, <-done) })
	return l.Addr().String(), s
}

func dialQDTLS(t *testing.T, addr string) *piondtls.Conn {
	t.Helper()
	// Use raw authenticated datagrams so no outbound client capability is assumed.
	c, err := piondtls.Dial("udp4", mustUDPAddr(t, addr), qDTLSConfig())
	require.NoError(t, err)
	require.NoError(t, c.SetDeadline(time.Now().Add(3*time.Second)))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func writeQDTLS(t *testing.T, c *piondtls.Conn, req *pool.Message) {
	t.Helper()
	b, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = c.Write(b)
	require.NoError(t, err)
}
func readQDTLS(t *testing.T, c *piondtls.Conn) *pool.Message {
	t.Helper()
	b := make([]byte, 2048)
	n, err := c.Read(b)
	require.NoError(t, err)
	m := pool.NewMessage(context.Background())
	_, err = m.UnmarshalWithDecoder(qblock.Decoder{}, b[:n])
	require.NoError(t, err)
	return m
}
func newQDTLSRequest(t *testing.T, code codes.Code) *pool.Message {
	t.Helper()
	m := pool.NewMessage(context.Background())
	m.SetCode(code)
	m.SetType(message.NonConfirmable)
	m.SetMessageID(100)
	m.SetToken([]byte{1})
	m.SetOptionBytes(message.RequestTag, []byte{9})
	require.NoError(t, m.SetPath("/q"))
	return m
}

func TestQBlockDTLSServerTransfers(t *testing.T) {
	for _, code := range []codes.Code{codes.GET, codes.POST, codes.PUT} {
		t.Run(code.String(), func(t *testing.T) {
			cfg := qblock.DefaultServerConfig()
			cfg.ProbingRate = 4096
			var calls atomic.Int32
			bodies := make(chan []byte, 2)
			addr, _ := startQDTLS(t, options.WithQBlockServer(cfg), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
				calls.Add(1)
				body, err := r.ReadBody()
				require.NoError(t, err)
				bodies <- body
				require.Equal(t, code, r.Code())
				require.Equal(t, []byte{1}, []byte(r.Token()))
				status := codes.Content
				if code != codes.GET {
					status = codes.Changed
				}
				require.NoError(t, w.SetResponse(status, message.TextPlain, bytes.NewReader([]byte("0123456789abcdefresponse"))))
			}))
			c := dialQDTLS(t, addr)
			req := newQDTLSRequest(t, code)
			req.SetOptionUint32(message.QBlock2, 8)
			if code == codes.GET {
				writeQDTLS(t, c, req)
			} else {
				req.SetOptionUint32(message.Size1, 20)
				req.SetOptionUint32(message.QBlock1, 16)
				req.SetBody(bytes.NewReader([]byte("tail")))
				writeQDTLS(t, c, req)
				req.SetMessageID(101)
				req.SetOptionUint32(message.QBlock1, 8)
				req.SetBody(bytes.NewReader([]byte("0123456789abcdef")))
				writeQDTLS(t, c, req)
			}
			var got []byte
			for len(got) < 24 {
				m := readQDTLS(t, c)
				require.True(t, m.HasOption(message.QBlock2))
				v, err := m.GetOptionUint32(message.QBlock2)
				require.NoError(t, err)
				require.Equal(t, uint32(len(got)/16), v>>4)
				require.Equal(t, message.NonConfirmable, m.Type())
				body, err := m.ReadBody()
				require.NoError(t, err)
				got = append(got, body...)
			}
			require.Equal(t, "0123456789abcdefresponse", string(got))
			select {
			case b := <-bodies:
				if code == codes.GET {
					require.Empty(t, b)
				} else {
					require.Equal(t, "0123456789abcdeftail", string(b))
				}
			case <-time.After(time.Second):
				t.Fatal("no assembled handler")
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestQBlockDTLSServerInvalidConfig(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxPeers = 0
	s := dtls.NewServer(options.WithQBlockServer(cfg))
	require.Error(t, s.Serve(nil))
	s.Stop()
	cfg = qblock.DefaultServerConfig()
	cfg.MaxOwnedBytes = 1
	s = dtls.NewServer(options.WithQBlockServer(cfg))
	require.Error(t, s.Serve(nil))
	s.Stop()
	cfg = qblock.DefaultServerConfig()
	s = dtls.NewServer(options.WithQBlockServer(cfg), options.WithMTU(0))
	require.Error(t, s.Serve(nil))
	s.Stop()
}

func TestQBlockDTLSServerClassicAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			opts := []server.Option{options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
				require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("classic"))))
			})}
			if enabled {
				opts = append(opts, options.WithQBlockServer(qblock.DefaultServerConfig()))
			}
			addr, _ := startQDTLS(t, opts...)
			c := dialQDTLS(t, addr)
			req := newQDTLSRequest(t, codes.GET)
			req.SetType(message.Confirmable)
			if !enabled {
				req.SetOptionUint32(message.QBlock2, 8)
			}
			writeQDTLS(t, c, req)
			resp := readQDTLS(t, c)
			require.False(t, resp.HasOption(message.QBlock2))
			if enabled {
				require.Equal(t, codes.Content, resp.Code())
			} else {
				require.Equal(t, codes.BadOption, resp.Code())
			}
		})
	}
}

func mustUDPAddr(t *testing.T, addr string) *net.UDPAddr {
	t.Helper()
	a, err := net.ResolveUDPAddr("udp4", addr)
	require.NoError(t, err)
	return a
}

func TestQBlockDTLSServerOversizeRetry(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.ProbingRate = 4096
	calls := make(chan struct{}, 2)
	addr, _ := startQDTLS(t, options.WithQBlockServer(cfg), options.WithMTU(64), options.WithMaxMessageSize(64), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
		calls <- struct{}{}
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	}))
	c := dialQDTLS(t, addr)
	req := newQDTLSRequest(t, codes.POST)
	req.SetOptionUint32(message.QBlock1, 0)
	req.SetOptionUint32(message.Size1, 4)
	req.SetBody(bytes.NewReader([]byte("body")))
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = c.Write(append(append([]byte(nil), wire...), bytes.Repeat([]byte("x"), 128)...))
	require.NoError(t, err)
	select {
	case <-calls:
		t.Fatal("oversized record dispatched")
	case <-time.After(50 * time.Millisecond):
	}
	writeQDTLS(t, c, req)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("valid retry lost after oversized record")
	}
}

func TestQBlockDTLSServerSessionReplacement(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.ProbingRate = 1 << 20
	calls := make(chan string, 4)
	addr, _ := startQDTLS(t, options.WithQBlockServer(cfg), options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		b, err := r.ReadBody()
		require.NoError(t, err)
		calls <- string(b)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
	}))
	for i := 0; i < 2; i++ {
		c := dialQDTLS(t, addr)
		req := newQDTLSRequest(t, codes.POST)
		req.SetOptionUint32(message.QBlock1, 0)
		req.SetOptionUint32(message.Size1, 4)
		req.SetBody(bytes.NewReader([]byte("body")))
		writeQDTLS(t, c, req)
		select {
		case b := <-calls:
			require.Equal(t, "body", b)
		case <-time.After(time.Second):
			t.Fatal("replacement retained suppression")
		}
		readQDTLS(t, c)
		require.NoError(t, c.Close())
	}
}
