package udp_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockCapabilityProbeUDP(t *testing.T) {
	for _, classic := range []bool{false, true} {
		t.Run(map[bool]string{false: "classic_disabled", true: "classic_enabled"}[classic], func(t *testing.T) {
			peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			defer peer.Close()
			require.NoError(t, peer.SetDeadline(time.Now().Add(3*time.Second)))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cc, err := udp.Dial(peer.LocalAddr().String(), options.WithContext(ctx), options.WithBlockwise(classic, blockwise.SZX16, time.Second))
			require.NoError(t, err)
			defer cc.Close()
			result := make(chan error, 1)
			go func() {
				supported, err := cc.ProbeQBlock(ctx, "/probe")
				if err == nil && !supported {
					err = net.ErrClosed
				}
				result <- err
			}()
			buf := make([]byte, 2048)
			n, addr, err := peer.ReadFromUDP(buf)
			require.NoError(t, err)
			req := pool.NewMessage(ctx)
			_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
			require.NoError(t, err)
			require.Equal(t, message.Confirmable, req.Type())
			require.Equal(t, codes.GET, req.Code())
			require.Nil(t, req.Body())
			path, err := req.Path()
			require.NoError(t, err)
			require.Equal(t, "/probe", path)
			value, err := req.GetOptionUint32(message.QBlock2)
			require.NoError(t, err)
			require.Zero(t, value)
			require.Len(t, req.Options(), 2)
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
			_, err = peer.WriteToUDP(wire, addr)
			require.NoError(t, err)
			require.NoError(t, <-result)
			// The next packet must be this explicitly issued ordinary GET, not a
			// continuation of the probe or an automatic switch to Q payload traffic.
			go func() {
				r, err := cc.Get(ctx, "/ordinary")
				if r != nil {
					cc.ReleaseMessage(r)
				}
				result <- err
			}()
			n, addr, err = peer.ReadFromUDP(buf)
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
			_, err = peer.WriteToUDP(wire, addr)
			require.NoError(t, err)
			require.NoError(t, <-result)
		})
	}
}

func TestQBlockOutboundUDP(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	require.NoError(t, peer.SetDeadline(time.Now().Add(4*time.Second)))
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cfg := qblock.DefaultClientConfig()
	cfg.Mode = qblock.Require
	cc, err := udp.Dial(peer.LocalAddr().String(), options.WithContext(ctx), options.WithQBlock(cfg), options.WithBlockwise(false, blockwise.SZX16, time.Second))
	require.NoError(t, err)
	defer cc.Close()
	_, err = cc.Get(ctx, "/x")
	require.ErrorIs(t, err, qblock.ErrCapabilityUnknown)
	done := make(chan error, 1)
	go func() {
		ok, err := cc.ProbeQBlock(ctx, "/probe")
		if !ok && err == nil {
			err = net.ErrClosed
		}
		done <- err
	}()
	buf := make([]byte, 2048)
	n, addr, err := peer.ReadFromUDP(buf)
	require.NoError(t, err)
	req := pool.NewMessage(ctx)
	_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	reply := pool.NewMessage(ctx)
	reply.SetType(message.Acknowledgement)
	reply.SetCode(codes.Content)
	reply.SetMessageID(req.MessageID())
	reply.SetToken(req.Token())
	reply.SetOptionUint32(message.QBlock2, 0)
	reply.SetOptionBytes(message.ETag, []byte{1})
	reply.SetOptionUint32(message.Size2, 2)
	reply.SetBody(bytes.NewReader([]byte("ok")))
	wire, err := reply.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(wire, addr)
	require.NoError(t, err)
	require.NoError(t, <-done)
	go func() {
		r, err := cc.Get(ctx, "/x")
		if r != nil {
			cc.ReleaseMessage(r)
		}
		done <- err
	}()
	n, addr, err = peer.ReadFromUDP(buf)
	require.NoError(t, err)
	req = pool.NewMessage(ctx)
	_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	require.Equal(t, message.NonConfirmable, req.Type())
	require.True(t, req.HasOption(message.QBlock2))
	reply.SetType(message.NonConfirmable)
	reply.SetMessageID(req.MessageID() + 1)
	reply.SetToken(req.Token())
	wire, err = reply.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(wire, addr)
	require.NoError(t, err)
	require.NoError(t, <-done)
}

func TestQBlockConstructionUDP(t *testing.T) {
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer socket.Close()
	cfg := qblock.DefaultClientConfig()
	cfg.MaxProbeWaiters = 0
	for _, owned := range []bool{false, true} {
		transport := socket
		if owned {
			transport, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
		}
		calls, periodic := 0, 0
		opts := []udp.Option{options.WithQBlock(cfg), options.WithErrors(func(error) { calls++ }), options.WithPeriodicRunner(func(func(time.Time) bool) { periodic++ })}
		if owned {
			opts = append(opts, options.WithCloseSocket())
		}
		cc := udp.Client(transport, opts...)
		require.Error(t, cc.InitializationError())
		select {
		case <-cc.Done():
		default:
			t.Fatal("failed Done open")
		}
		require.Equal(t, 1, calls)
		require.Zero(t, periodic)
		assertQBlockInitializationGuardsUDP(t, cc)
		require.Equal(t, 1, calls)
		err = transport.SetReadDeadline(time.Now())
		if owned {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	_, err = udp.Dial("invalid target", options.WithQBlock(cfg))
	require.Error(t, err)
}

func assertQBlockInitializationGuardsUDP(t *testing.T, cc *client.Conn) {
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

func TestQBlockDialRejectsBeforeSocketUDP(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	q := qblock.DefaultClientConfig()
	q.MaxProbeWaiters = 0
	var opens int
	dialer := &net.Dialer{Control: func(_, _ string, _ syscall.RawConn) error { opens++; return nil }}
	_, err = udp.Dial(peer.LocalAddr().String(), options.WithQBlock(q), options.WithDialer(dialer))
	require.Error(t, err)
	require.Zero(t, opens)
}

func TestQBlockSuppliedClientKeepsRuntimeErrorsUDP(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	clientSocket, err := net.DialUDP("udp4", nil, peer.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer clientSocket.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reported := make(chan error, 1)
	sentinel := errors.New("supplied UDP monitor failure")
	cc := udp.Client(clientSocket, options.WithContext(ctx), options.WithQBlock(qblock.DefaultClientConfig()), options.WithErrors(func(err error) { reported <- err }), runtimeMonitorOption{sentinel})
	defer cc.Close()
	req := pool.NewMessage(ctx)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(wire, cc.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	select {
	case err := <-reported:
		require.ErrorIs(t, err, sentinel)
	case <-time.After(time.Second):
		t.Fatal("supplied UDP Client discarded runtime Errors callback")
	}
}

func TestQBlockDialKeepsRuntimeErrors(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reported := make(chan error, 1)
	sentinel := errors.New("request monitor failure")
	cc, err := udp.Dial(peer.LocalAddr().String(), options.WithContext(ctx), options.WithErrors(func(err error) { reported <- err }), runtimeMonitorOption{sentinel})
	require.NoError(t, err)
	defer cc.Close()
	req := pool.NewMessage(ctx)
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.GET)
	req.SetToken([]byte{1})
	req.SetMessageID(1)
	wire, err := req.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(wire, cc.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	select {
	case err := <-reported:
		require.ErrorIs(t, err, sentinel)
	case <-time.After(time.Second):
		t.Fatal("Dial discarded runtime Errors callback")
	}
}

type runtimeMonitorOption struct{ err error }

func (o runtimeMonitorOption) UDPClientApply(c *client.Config) {
	c.RequestMonitor = func(*client.Conn, *pool.Message) (bool, error) { return false, o.err }
}

func TestQBlockNoReplayAfterPeerProcessesPOST(t *testing.T) {
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer peer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	q := qblock.DefaultClientConfig()
	q.Mode = qblock.Require
	cc, err := udp.Dial(peer.LocalAddr().String(), options.WithContext(ctx), options.WithQBlock(q))
	require.NoError(t, err)
	defer cc.Close()
	probe := make(chan error, 1)
	go func() { _, err := cc.ProbeQBlock(ctx, ""); probe <- err }()
	buf := make([]byte, 2048)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(time.Second)))
	n, addr, err := peer.ReadFromUDP(buf)
	require.NoError(t, err)
	req := pool.NewMessage(ctx)
	_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	reply := pool.NewMessage(ctx)
	reply.SetCode(codes.Content)
	reply.SetType(message.Acknowledgement)
	reply.SetMessageID(req.MessageID())
	reply.SetToken(req.Token())
	reply.SetOptionUint32(message.QBlock2, 0)
	reply.SetOptionBytes(message.ETag, []byte{1})
	reply.SetOptionUint32(message.Size2, 1)
	reply.SetBody(bytes.NewReader([]byte("x")))
	wire, err := reply.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = peer.WriteToUDP(wire, addr)
	require.NoError(t, err)
	require.NoError(t, <-probe)
	operation, cancelOperation := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancelOperation()
	result := make(chan error, 1)
	go func() {
		r, err := cc.Post(operation, "/write", message.TextPlain, bytes.NewReader([]byte("x")))
		if r != nil {
			cc.ReleaseMessage(r)
		}
		result <- err
	}()
	n, _, err = peer.ReadFromUDP(buf)
	require.NoError(t, err)
	req = pool.NewMessage(ctx)
	_, err = req.UnmarshalWithDecoder(qblock.Decoder{}, buf[:n])
	require.NoError(t, err)
	require.Equal(t, codes.POST, req.Code())
	require.True(t, req.HasOption(message.QBlock1))
	payload, err := io.ReadAll(req.Body())
	require.NoError(t, err)
	require.Equal(t, []byte("x"), payload)
	// The independent peer applies this complete one-block operation once, drops
	// its terminal response, then watches for any automatic resubmission.
	applied := 1
	require.ErrorIs(t, <-result, context.DeadlineExceeded)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(50*time.Millisecond)))
	_, _, err = peer.ReadFromUDP(buf)
	require.Error(t, err)
	require.Equal(t, 1, applied)
}
