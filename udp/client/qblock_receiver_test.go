package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type qblockTestSession struct {
	ctx       context.Context
	writeType message.Type
	writeQ2   bool
}

func (s *qblockTestSession) Context() context.Context { return s.ctx }
func (s *qblockTestSession) Close() error             { return nil }
func (s *qblockTestSession) MaxMessageSize() uint32   { return 2048 }
func (s *qblockTestSession) RemoteAddr() net.Addr     { return &net.UDPAddr{} }
func (s *qblockTestSession) LocalAddr() net.Addr      { return &net.UDPAddr{} }
func (s *qblockTestSession) NetConn() net.Conn        { return nil }
func (s *qblockTestSession) WriteMessage(msg *pool.Message) error {
	s.writeType = msg.Type()
	s.writeQ2 = msg.HasOption(message.QBlock2)
	return nil
}
func (s *qblockTestSession) WriteMulticastMessage(*pool.Message, *net.UDPAddr, ...coapNet.MulticastOption) error {
	return nil
}
func (s *qblockTestSession) Run(*Conn) error                          { return nil }
func (s *qblockTestSession) AddOnClose(EventFunc)                     {}
func (s *qblockTestSession) SetContextValue(interface{}, interface{}) {}
func (s *qblockTestSession) Done() <-chan struct{}                    { return nil }

func TestQBlockPrepareInitialGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX64
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockReceiver(qblockReceiverConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(message.Token{1, 2, 3})
	require.NoError(t, req.SetPath("/temperature"))

	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	require.Equal(t, message.NonConfirmable, req.Type())
	value, err := req.GetOptionUint32(message.QBlock2)
	require.NoError(t, err)
	block, err := qblock.DecodeBlock(value)
	require.NoError(t, err)
	require.Equal(t, qblock.Block{Number: 0, More: true, SZX: blockwise.SZX64}, block)
	require.Zero(t, cc.qblockReceiver.active())
}

func TestDoInternalPreparesPrivateQBlockGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX64
	session := &qblockTestSession{ctx: context.Background()}
	cc := NewConnWithOpts(session, &cfg,
		withQBlockReceiver(qblockReceiverConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(message.Token{4, 5, 6})
	require.NoError(t, req.SetPath("/temperature"))

	_, err := cc.doInternal(req)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, message.NonConfirmable, session.writeType)
	require.True(t, session.writeQ2)
}

func TestQBlockFirstFragmentRollback(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{8, 9, 10})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	invalid := newQBlockResponse(t, cc, req.Token(), false)
	defer cc.ReleaseMessage(invalid)
	require.True(t, cc.qblockReceiver.handle(invalid))
	require.Zero(t, cc.qblockReceiver.active())

	valid := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(valid)
	require.True(t, cc.qblockReceiver.handle(valid))
	require.Equal(t, uint32(1), cc.qblockReceiver.active())
}

func TestConnRoutesQBlockResponseBeforeTokenHandler(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{11, 12, 13})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	called := false
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
		called = true
	})
	response := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(response)
	writerMessage := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(writerMessage)

	cc.handle(responsewriter.New(writerMessage, cc), response)

	require.False(t, called)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())
}

func TestConnDeliversCompleteQBlockResponseThroughOriginalHandler(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{14, 15, 16})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	var delivered *pool.Message
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		delivered = msg
	})
	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	last := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(last)
	last.SetCode(codes.Content)
	last.SetToken(req.Token())
	value, err := qblock.EncodeBlock(qblock.Block{Number: 1, More: false, SZX: blockwise.SZX16})
	require.NoError(t, err)
	last.SetOptionUint32(message.QBlock2, value)
	last.SetOptionUint32(message.Size2, 32)
	require.NoError(t, last.SetETag([]byte("etag-a")))
	last.SetBody(bytes.NewReader(bytes.Repeat([]byte{'b'}, 16)))
	writerMessage := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(writerMessage)

	cc.handle(responsewriter.New(writerMessage, cc), first)
	cc.handle(responsewriter.New(writerMessage, cc), last)

	require.NotNil(t, delivered)
	defer cc.ReleaseMessage(delivered)
	body, err := io.ReadAll(delivered.Body())
	require.NoError(t, err)
	require.Equal(t, append(bytes.Repeat([]byte{'a'}, 16), bytes.Repeat([]byte{'b'}, 16)...), body)
	require.Equal(t, req.Token(), delivered.Token())
	require.Zero(t, cc.qblockReceiver.active())
}

func newPrivateQBlockConn(t *testing.T) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	return NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockReceiver(qblockReceiverConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
}

func newPrivateQBlockGET(t *testing.T, cc *Conn, token message.Token) *pool.Message {
	t.Helper()
	req := cc.AcquireMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken(token)
	require.NoError(t, req.SetPath("/temperature"))
	return req
}

func newQBlockResponse(t *testing.T, cc *Conn, token message.Token, withETag bool) *pool.Message {
	t.Helper()
	resp := cc.AcquireMessage(context.Background())
	resp.SetCode(codes.Content)
	resp.SetToken(token)
	value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16})
	require.NoError(t, err)
	resp.SetOptionUint32(message.QBlock2, value)
	resp.SetOptionUint32(message.Size2, 32)
	if withETag {
		require.NoError(t, resp.SetETag([]byte("etag-a")))
	}
	resp.SetBody(bytes.NewReader(bytes.Repeat([]byte{'a'}, 16)))
	return resp
}
