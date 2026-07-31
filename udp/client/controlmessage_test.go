package client

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestSetControlInformationNil(t *testing.T) {
	var cc Conn
	addr := net.ParseIP("192.0.2.1").To4()
	cc.localAddr.Store(&addr)
	cc.interfaceIndex.Store(7)

	cc.setControlInformation(nil)

	require.Equal(t, int64(0), cc.interfaceIndex.Load())
	require.Nil(t, cc.localAddr.Load())
}

func TestSetControlInformationUnicastCopiesAddress(t *testing.T) {
	var cc Conn
	dst := net.ParseIP("2001:db8::1")
	cm := &coapNet.ControlMessage{Dst: append(net.IP(nil), dst...), IfIndex: 9}

	cc.setControlInformation(cm)

	require.Equal(t, int64(9), cc.interfaceIndex.Load())
	stored := cc.localAddr.Load()
	require.NotNil(t, stored)
	expected := append(net.IP(nil), dst...)
	require.True(t, stored.Equal(expected))

	cm.Dst[0] ^= 0xff
	require.True(t, stored.Equal(expected))
}

func TestSetControlInformationMulticastClearsAddress(t *testing.T) {
	var cc Conn
	addr := net.ParseIP("192.0.2.1").To4()
	cc.localAddr.Store(&addr)

	cc.setControlInformation(&coapNet.ControlMessage{Dst: net.ParseIP("ff02::1"), IfIndex: 11})

	require.Equal(t, int64(11), cc.interfaceIndex.Load())
	require.Nil(t, cc.localAddr.Load())
}

func TestUpsertControlInformationUsesOriginalDestinationAsSrc(t *testing.T) {
	// Fly.io-style path: client sends to a public IP while the socket is bound to a
	// private address. The received ControlMessage.Dst is the public IP and must be
	// reused as ControlMessage.Src on the response.
	var cc Conn
	publicIP := net.ParseIP("203.0.113.10").To4()
	cc.setControlInformation(&coapNet.ControlMessage{Dst: publicIP, IfIndex: 3})

	msg := pool.NewMessage(context.Background())
	cc.upsertControlInformation(msg)

	cm := msg.ControlMessage()
	require.NotNil(t, cm)
	require.Equal(t, 3, cm.IfIndex)
	require.True(t, cm.Src.Equal(publicIP))
}

func TestUpsertControlInformationIsNoopWithoutStoredState(t *testing.T) {
	var cc Conn
	msg := pool.NewMessage(context.Background())
	cc.upsertControlInformation(msg)
	require.Nil(t, msg.ControlMessage())
}

func TestUpsertControlInformationDoesNotOverrideExistingControlMessage(t *testing.T) {
	var cc Conn
	publicIP := net.ParseIP("203.0.113.10").To4()
	cc.setControlInformation(&coapNet.ControlMessage{Dst: publicIP, IfIndex: 3})

	msg := pool.NewMessage(context.Background())
	existing := &coapNet.ControlMessage{Src: net.ParseIP("198.51.100.1").To4(), IfIndex: 9}
	msg.SetControlMessage(existing)

	cc.upsertControlInformation(msg)

	cm := msg.ControlMessage()
	require.Same(t, existing, cm)
	require.True(t, cm.Src.Equal(net.ParseIP("198.51.100.1").To4()))
	require.Equal(t, 9, cm.IfIndex)
}

// recordingSession captures WriteMessage payloads for control-message assertions.
type recordingSession struct {
	mu      sync.Mutex
	written []*pool.Message
	ctx     context.Context
	cancel  context.CancelFunc
	raddr   *net.UDPAddr
	done    chan struct{}
}

func newRecordingSession() *recordingSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &recordingSession{
		ctx:    ctx,
		cancel: cancel,
		raddr:  &net.UDPAddr{IP: net.ParseIP("192.0.2.50").To4(), Port: 5683},
		done:   make(chan struct{}),
	}
}

func (s *recordingSession) Context() context.Context { return s.ctx }
func (s *recordingSession) Close() error {
	s.cancel()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}
func (s *recordingSession) MaxMessageSize() uint32 { return 64 * 1024 }
func (s *recordingSession) RemoteAddr() net.Addr   { return s.raddr }
func (s *recordingSession) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("10.0.0.2").To4(), Port: 5683}
}
func (s *recordingSession) NetConn() net.Conn { return nil }
func (s *recordingSession) WriteMessage(req *pool.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Capture a snapshot of the control message; the pool may reuse the Message later.
	cloned := pool.NewMessage(req.Context())
	if err := req.Clone(cloned); err != nil {
		return err
	}
	s.written = append(s.written, cloned)
	return nil
}

func (s *recordingSession) WriteMulticastMessage(*pool.Message, *net.UDPAddr, ...coapNet.MulticastOption) error {
	return nil
}

func (s *recordingSession) Run(*Conn) error {
	<-s.done
	return nil
}
func (s *recordingSession) AddOnClose(func())                        {}
func (s *recordingSession) SetContextValue(interface{}, interface{}) {}
func (s *recordingSession) Done() <-chan struct{}                    { return s.done }

func (s *recordingSession) lastWritten() *pool.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.written) == 0 {
		return nil
	}
	return s.written[len(s.written)-1]
}

func (s *recordingSession) writtenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.written)
}

func newTestConnWithSession(t *testing.T) (*Conn, *recordingSession) {
	t.Helper()
	session := newRecordingSession()
	cfg := DefaultConfig
	cfg.Errors = func(error) {}
	cfg.BlockwiseEnable = false
	// createBlockWise defaults to a nil-returning function via NewConnWithOpts.
	cc := NewConnWithOpts(session, &cfg)
	t.Cleanup(func() {
		_ = session.Close()
	})
	return cc, session
}

func TestProcessReceivedMessageRepliesFromOriginalDestinationIP(t *testing.T) {
	// Regression for Fly.io / multi-homed UDP: when a request arrives with
	// ControlMessage.Dst set to the public (or otherwise selected) destination IP,
	// the response must set ControlMessage.Src to that same IP so the kernel
	// sources the reply correctly.
	cc, session := newTestConnWithSession(t)

	publicIP := net.ParseIP("203.0.113.10").To4()
	req := cc.AcquireMessage(context.Background())
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	token, err := message.GetToken()
	require.NoError(t, err)
	req.SetToken(token)
	req.SetMessageID(42)
	req.SetControlMessage(&coapNet.ControlMessage{Dst: publicIP, IfIndex: 5})

	done := make(chan struct{})
	cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		require.Equal(t, codes.GET, r.Code())
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, nil))
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler was not invoked")
	}

	require.Eventually(t, func() bool {
		return session.writtenCount() >= 1
	}, time.Second, 5*time.Millisecond)

	written := session.lastWritten()
	require.NotNil(t, written)
	cm := written.ControlMessage()
	require.NotNil(t, cm, "response must carry a control message with Src set")
	require.True(t, cm.Src.Equal(publicIP), "Src=%v want %v", cm.Src, publicIP)
	require.Equal(t, 5, cm.IfIndex)
}

func TestWriteMessageUsesStoredOriginalDestinationIP(t *testing.T) {
	cc, session := newTestConnWithSession(t)

	publicIP := net.ParseIP("203.0.113.10").To4()
	// Simulate a previously received request that taught the conn its public local IP.
	cc.setControlInformation(&coapNet.ControlMessage{Dst: publicIP, IfIndex: 2})

	req := cc.AcquireMessage(context.Background())
	req.SetType(message.NonConfirmable)
	req.SetCode(codes.Content)
	token, err := message.GetToken()
	require.NoError(t, err)
	req.SetToken(token)
	req.SetMessageID(7)

	require.NoError(t, cc.WriteMessage(req))

	written := session.lastWritten()
	require.NotNil(t, written)
	cm := written.ControlMessage()
	require.NotNil(t, cm)
	require.True(t, cm.Src.Equal(publicIP), "Src=%v want %v", cm.Src, publicIP)
	require.Equal(t, 2, cm.IfIndex)
}

func TestAsyncPingUsesStoredOriginalDestinationIP(t *testing.T) {
	cc, session := newTestConnWithSession(t)

	publicIP := net.ParseIP("203.0.113.10").To4()
	cc.setControlInformation(&coapNet.ControlMessage{Dst: publicIP, IfIndex: 4})

	cancel, err := cc.AsyncPing(func() {})
	require.NoError(t, err)
	t.Cleanup(cancel)

	written := session.lastWritten()
	require.NotNil(t, written)
	cm := written.ControlMessage()
	require.NotNil(t, cm, "keepalive ping must carry control message with Src set")
	require.True(t, cm.Src.Equal(publicIP), "Src=%v want %v", cm.Src, publicIP)
	require.Equal(t, 4, cm.IfIndex)
	require.Equal(t, codes.Empty, written.Code())
	require.Equal(t, message.Confirmable, written.Type())
}
