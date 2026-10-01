package dtls_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
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
	"github.com/plgd-dev/go-coap/v3/udp"
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

func establishQDTLSTransport(t *testing.T, listener net.Listener) (*piondtls.Conn, *piondtls.Conn) {
	t.Helper()
	transport, err := piondtls.Dial("udp4", listener.Addr().(*net.UDPAddr), qDTLSConfig())
	require.NoError(t, err)
	writeDone := make(chan error, 1)
	go func() { _, err := transport.Write([]byte("handshake")); writeDone <- err }()
	accepted, err := listener.Accept()
	require.NoError(t, err)
	peer, ok := accepted.(*piondtls.Conn)
	require.True(t, ok)
	buf := make([]byte, 64)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	n, err := peer.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "handshake", string(buf[:n]))
	require.NoError(t, <-writeDone)
	return transport, peer
}

func TestQBlockConstructionDTLS(t *testing.T) {
	q := qblock.DefaultClientConfig()
	q.MaxProbeWaiters = 0
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "borrowed", true: "CloseSocket"}[owned], func(t *testing.T) {
			listener, err := piondtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, qDTLSConfig())
			require.NoError(t, err)
			defer listener.Close()
			transport, peer := establishQDTLSTransport(t, listener)
			defer peer.Close()
			calls := atomic.Int32{}
			periodic := atomic.Int32{}
			opts := []udp.Option{options.WithQBlock(q), options.WithErrors(func(error) { calls.Add(1) }), options.WithPeriodicRunner(func(func(time.Time) bool) { periodic.Add(1) })}
			if owned {
				opts = append(opts, options.WithCloseSocket())
			}
			cc := dtls.Client(transport, opts...)
			require.Error(t, cc.InitializationError())
			select {
			case <-cc.Done():
			default:
				t.Fatal("failed DTLS construction returned with Done open")
			}
			require.EqualValues(t, 1, calls.Load())
			require.Zero(t, periodic.Load())
			assertQBlockInitializationGuardsDTLS(t, cc)
			require.EqualValues(t, 1, calls.Load())
			require.Zero(t, periodic.Load())
			if owned {
				_, err = transport.Write([]byte{1})
				require.Error(t, err)
			} else {
				require.NoError(t, transport.SetWriteDeadline(time.Now().Add(time.Second)))
				_, err = transport.Write([]byte("borrowed socket remains open"))
				require.NoError(t, err)
				buf := make([]byte, 64)
				require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
				n, readErr := peer.Read(buf)
				require.NoError(t, readErr)
				require.Equal(t, "borrowed socket remains open", string(buf[:n]))
			}
			_ = cc.Close()
			if !owned {
				_ = transport.Close()
			}
		})
	}
}

func assertQBlockInitializationGuardsDTLS(t *testing.T, cc *client.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := pool.NewMessage(ctx)
	req.SetCode(codes.GET)
	req.SetType(message.NonConfirmable)
	observeReq := pool.NewMessage(ctx)
	observeReq.SetCode(codes.GET)
	observeReq.SetType(message.Confirmable)
	observeReq.SetObserve(0)
	cases := []struct {
		name string
		run  func() error
	}{
		{"Do", func() error { _, err := cc.Do(req); return err }},
		{"Get", func() error { _, err := cc.Get(ctx, "/x"); return err }},
		{"Post", func() error {
			_, err := cc.Post(ctx, "/x", message.TextPlain, bytes.NewReader([]byte("body")))
			return err
		}},
		{"Put", func() error {
			_, err := cc.Put(ctx, "/x", message.TextPlain, bytes.NewReader([]byte("body")))
			return err
		}},
		{"Delete", func() error { _, err := cc.Delete(ctx, "/x"); return err }},
		{"DoObserve", func() error { _, err := cc.DoObserve(observeReq, func(*pool.Message) {}); return err }},
		{"Observe", func() error { _, err := cc.Observe(ctx, "/x", func(*pool.Message) {}); return err }},
		{"ProbeQBlock", func() error { _, err := cc.ProbeQBlock(ctx, "/x"); return err }},
		{"WriteMessage", func() error { return cc.WriteMessage(req) }},
		{"AsyncPing", func() error { _, err := cc.AsyncPing(func() {}); return err }},
		{"Ping", func() error { return cc.Ping(ctx) }},
		{"Run", cc.Run},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, tc.run(), cc.InitializationError())
		})
	}
}

func TestQBlockDialRejectsBeforeSocketDTLS(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	q := qblock.DefaultClientConfig()
	q.MaxProbeWaiters = 0
	var opens int
	dialer := &net.Dialer{Control: func(_, _ string, _ syscall.RawConn) error { opens++; return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = dtls.Dial(peer.LocalAddr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithQBlock(q), options.WithDialer(dialer))
	require.Error(t, err)
	require.Zero(t, opens)
}

func TestQBlockSuppliedClientKeepsRuntimeErrorsDTLS(t *testing.T) {
	listener, err := piondtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, qDTLSConfig())
	require.NoError(t, err)
	defer listener.Close()
	transport, peer := establishQDTLSTransport(t, listener)
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reported := make(chan error, 1)
	sentinel := errors.New("supplied DTLS monitor failure")
	cc := dtls.Client(transport, options.WithContext(ctx), options.WithQBlock(qblock.DefaultClientConfig()), options.WithErrors(func(err error) { reported <- err }), dtlsRuntimeMonitorOption{sentinel})
	defer cc.Close()
	req := pool.NewMessage(ctx)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.Write(wire)
	require.NoError(t, err)
	select {
	case err := <-reported:
		require.ErrorIs(t, err, sentinel)
	case <-time.After(time.Second):
		t.Fatal("supplied DTLS Client discarded runtime Errors callback")
	}
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

func TestQBlockOutboundDTLS(t *testing.T) {
	for _, classic := range []bool{false, true} {
		t.Run(map[bool]string{false: "classic_disabled", true: "classic_enabled"}[classic], func(t *testing.T) {
			l, err := piondtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, qDTLSConfig())
			require.NoError(t, err)
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cc, err := dtls.Dial(l.Addr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithQBlock(qblock.DefaultClientConfig()), options.WithBlockwise(classic, blockwise.SZX16, time.Second))
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
			require.True(t, req.HasOption(message.QBlock2))
			require.Equal(t, message.NonConfirmable, req.Type())
			resp = pool.NewMessage(ctx)
			resp.SetType(message.NonConfirmable)
			resp.SetCode(codes.Content)
			resp.SetMessageID(req.MessageID())
			resp.SetToken(req.Token())
			resp.SetOptionUint32(message.QBlock2, 0)
			resp.SetOptionBytes(message.ETag, []byte{1})
			resp.SetOptionUint32(message.Size2, 8)
			resp.SetBody(bytes.NewReader([]byte("ordinary")))
			wire, err = resp.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			_, err = peer.Write(wire)
			require.NoError(t, err)
			require.NoError(t, <-result)
		})
	}
}

func TestQBlockDialKeepsRuntimeErrorsDTLS(t *testing.T) {
	l, err := piondtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, qDTLSConfig())
	require.NoError(t, err)
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	reported := make(chan error, 1)
	sentinel := errors.New("dtls monitor failure")
	cc, err := dtls.Dial(l.Addr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithErrors(func(err error) { reported <- err }), dtlsRuntimeMonitorOption{sentinel})
	require.NoError(t, err)
	defer cc.Close()
	handshake := make(chan error, 1)
	go func() { handshake <- cc.WriteMessage(pool.NewMessage(ctx)) }()
	peer, err := l.Accept()
	require.NoError(t, err)
	defer peer.Close()
	buf := make([]byte, 128)
	_, err = peer.Read(buf)
	require.NoError(t, err)
	<-handshake
	req := pool.NewMessage(ctx)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.Write(wire)
	require.NoError(t, err)
	select {
	case err := <-reported:
		require.ErrorIs(t, err, sentinel)
	case <-time.After(time.Second):
		t.Fatal("DTLS Dial discarded callback")
	}
}

type dtlsRuntimeMonitorOption struct{ err error }

func (o dtlsRuntimeMonitorOption) UDPClientApply(c *client.Config) {
	c.RequestMonitor = func(*client.Conn, *pool.Message) (bool, error) { return false, o.err }
}
