package server

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type qOption func(*Config)

func (o qOption) DTLSServerApply(c *Config) { o(c) }

type qRecord struct {
	data []byte
	err  error
}
type qDatagramConn struct {
	records  chan qRecord
	closed   chan struct{}
	didClose atomic.Bool
	peerPort int
}

func newQDatagramConn() *qDatagramConn {
	return &qDatagramConn{records: make(chan qRecord, 8), closed: make(chan struct{})}
}
func (c *qDatagramConn) Read(b []byte) (int, error) {
	select {
	case r := <-c.records:
		if r.err != nil {
			return 0, r.err
		}
		return copy(b, r.data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}
func (c *qDatagramConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *qDatagramConn) Close() error {
	if c.didClose.CompareAndSwap(false, true) {
		close(c.closed)
	}
	return nil
}
func (c *qDatagramConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5684}
}
func (c *qDatagramConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000 + c.peerPort}
}
func (c *qDatagramConn) SetDeadline(time.Time) error      { return nil }
func (c *qDatagramConn) SetReadDeadline(time.Time) error  { return nil }
func (c *qDatagramConn) SetWriteDeadline(time.Time) error { return nil }

type qListener struct {
	conns    chan net.Conn
	closed   chan struct{}
	didClose atomic.Bool
}

func (l *qListener) AcceptWithContext(ctx context.Context) (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closed:
		return nil, coapNet.ErrListenerIsClosed
	}
}
func (l *qListener) Close() error {
	if l.didClose.CompareAndSwap(false, true) {
		close(l.closed)
	}
	return nil
}

func TestQBlockDTLSServerConnectionLimit(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxConnections = 1
	cfg.ProbingRate = 4096
	admitted := make(chan *client.Conn, 8)
	s := New(qOption(func(c *Config) { c.QBlockServer = &cfg; c.OnNewConn = func(cc *client.Conn) { admitted <- cc } }))
	l := &qListener{conns: make(chan net.Conn, 8), closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	defer func() { s.Stop(); require.NoError(t, <-done) }()
	a := newQDatagramConn()
	l.conns <- a
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("first admission failed")
	}
	b := newQDatagramConn()
	defer b.Close()
	l.conns <- b
	select {
	case <-b.closed:
	case <-admitted:
		t.Fatal("over-limit connection admitted")
	case <-time.After(time.Second):
		t.Fatal("over-limit connection not closed")
	}
	require.NoError(t, a.Close())
	// Worker completion releases admission; a new peer must eventually succeed.
	require.Eventually(t, func() bool {
		c := newQDatagramConn()
		l.conns <- c
		select {
		case <-admitted:
			return true
		case <-c.closed:
			return false
		case <-time.After(50 * time.Millisecond):
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestQBlockDTLSSessionOversize(t *testing.T) {
	for _, mode := range []string{"overflow", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			cfg := qblock.DefaultServerConfig()
			cfg.ProbingRate = 4096
			calls := make(chan codes.Code, 4)
			s := New(qOption(func(c *Config) {
				c.QBlockServer = &cfg
				c.MTU = 128
				c.MaxMessageSize = 64
				c.Handler = func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
					calls <- r.Code()
					require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, nil))
				}
			}))
			defer s.Stop()
			socket := newQDatagramConn()
			cc, err := s.createConn(coapNet.NewConn(socket), client.DefaultConfig.CreateInactivityMonitor(), client.DefaultConfig.RequestMonitor)
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- cc.Run() }()
			defer func() { _ = socket.Close(); <-done }()
			req := pool.NewMessage(context.Background())
			req.SetType(message.NonConfirmable)
			req.SetCode(codes.POST)
			req.SetMessageID(1)
			req.SetToken([]byte{1})
			req.SetOptionBytes(message.RequestTag, []byte{9})
			req.SetOptionUint32(message.QBlock1, 0)
			req.SetOptionUint32(message.Size1, 4)
			req.SetBody(bytes.NewReader([]byte("body")))
			wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			if mode == "overflow" {
				socket.records <- qRecord{data: append(append([]byte(nil), wire...), bytes.Repeat([]byte("x"), 100)...)}
			} else {
				socket.records <- qRecord{err: &piondtls.TemporaryError{Err: io.ErrShortBuffer}}
			}
			socket.records <- qRecord{data: wire}
			select {
			case code := <-calls:
				require.Equal(t, codes.POST, code)
			case <-time.After(time.Second):
				t.Fatal("valid retry not dispatched")
			}
			ordinary := pool.NewMessage(context.Background())
			ordinary.SetType(message.NonConfirmable)
			ordinary.SetCode(codes.GET)
			ordinary.SetMessageID(2)
			ordinary.SetToken([]byte{2})
			b, err := ordinary.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			socket.records <- qRecord{data: b}
			select {
			case code := <-calls:
				require.Equal(t, codes.GET, code)
			case <-time.After(time.Second):
				t.Fatal("ordinary request lost")
			}
		})
	}
}

func TestQBlockDTLSRejectedConstructionCompletesSession(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxPeers = 1
	s := New(qOption(func(c *Config) { c.QBlockServer = &cfg }))
	defer s.Stop()
	c := client.DefaultConfig
	first := NewSession(s.ctx, coapNet.NewConn(newQDatagramConn()), 65536, c.MTU, true)
	cc, err := s.newSessionConn(first, &c, nil)
	require.NoError(t, err)
	defer func() { _ = cc.Close(); first.shutdown() }()
	// Exhaust the peer table while leaving worker capacity available.
	other := newQDatagramConn()
	other.peerPort = 1
	rejected := NewSession(s.ctx, coapNet.NewConn(other), 65536, c.MTU, true)
	_, err = s.newSessionConn(rejected, &c, nil)
	require.Error(t, err)
	select {
	case <-rejected.Done():
	case <-time.After(time.Second):
		t.Fatal("endpoint rejection never completed")
	}
	// Closing the domain simulates Stop before attachment, while the first Conn
	// remains constructed. Repeated rejection must finalize every session.
	s.Stop()
	for i := 0; i < 3; i++ {
		session := NewSession(s.ctx, coapNet.NewConn(newQDatagramConn()), 65536, c.MTU, true)
		var closed atomic.Int32
		session.AddOnClose(func() { closed.Add(1) })
		_, err := s.newSessionConn(session, &c, nil)
		require.Error(t, err)
		select {
		case <-session.Done():
		case <-time.After(time.Second):
			t.Fatal("rejected session never completed")
		}
		session.shutdown()
		require.Equal(t, int32(1), closed.Load())
	}
}

func TestQBlockDTLSStopBeforePublication(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	s := New(qOption(func(c *Config) { c.QBlockServer = &cfg }))
	defer s.Stop()
	c := client.DefaultConfig
	session := NewSession(s.ctx, coapNet.NewConn(newQDatagramConn()), 65536, c.MTU, true)
	cc, err := s.newSessionConn(session, &c, nil)
	require.NoError(t, err)
	var closed atomic.Int32
	session.AddOnClose(func() { closed.Add(1) })
	// Attachment and construction have succeeded, but publication has not.
	s.Stop()
	require.ErrorIs(t, s.admitQConnection(cc, session), context.Canceled)
	select {
	case <-cc.Done():
	case <-time.After(time.Second):
		t.Fatal("canceled construction never completed")
	}
	session.shutdown()
	require.Equal(t, int32(1), closed.Load())
}

func TestQBlockOutboundOnlyDTLSReceive(t *testing.T) {
	for _, mode := range []string{"overflow", "temporary"} {
		t.Run(mode, func(t *testing.T) {
			q := qblock.DefaultClientConfig()
			calls := make(chan codes.Code, 1)
			s := New(qOption(func(c *Config) {
				c.QBlock = &q
				c.MTU = 128
				c.MaxMessageSize = 64
				c.Handler = func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
					calls <- r.Code()
					_ = w.SetResponse(codes.Content, message.TextPlain, nil)
				}
			}))
			defer s.Stop()
			socket := newQDatagramConn()
			cc, err := s.createConn(coapNet.NewConn(socket), client.DefaultConfig.CreateInactivityMonitor(), client.DefaultConfig.RequestMonitor)
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { done <- cc.Run() }()
			defer func() { _ = socket.Close(); <-done }()
			req := pool.NewMessage(context.Background())
			req.SetType(message.NonConfirmable)
			req.SetCode(codes.GET)
			req.SetMessageID(1)
			req.SetToken([]byte{1})
			wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			if mode == "overflow" {
				socket.records <- qRecord{data: append(append([]byte(nil), wire...), bytes.Repeat([]byte{0}, 64)...)}
			} else {
				socket.records <- qRecord{err: &piondtls.TemporaryError{Err: io.ErrShortBuffer}}
			}
			socket.records <- qRecord{data: wire}
			select {
			case code := <-calls:
				require.Equal(t, codes.GET, code)
			case <-time.After(time.Second):
				t.Fatal("outbound-only reader lost valid retry")
			}
		})
	}
}
