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
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

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
