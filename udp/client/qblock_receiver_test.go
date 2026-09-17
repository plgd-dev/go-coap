package client

import (
	"bytes"
	"context"
	"errors"
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
	writes    []qblockTestWrite
	onClose   []EventFunc
	writeErr  error
	writeCh   chan struct{}
}

type qblockTestWrite struct {
	code    codes.Code
	typ     message.Type
	token   message.Token
	block   uint32
	mid     int32
	options message.Options
	payload []byte
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
	var block uint32
	if s.writeQ2 {
		value, err := msg.GetOptionUint32(message.QBlock2)
		if err != nil {
			return err
		}
		block = value
	}
	options, err := msg.Options().Clone()
	if err != nil {
		return err
	}
	var payload []byte
	if body := msg.Body(); body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return err
		}
	}
	s.writes = append(s.writes, qblockTestWrite{
		code:    msg.Code(),
		typ:     msg.Type(),
		token:   bytes.Clone(msg.Token()),
		block:   block,
		mid:     msg.MessageID(),
		options: options,
		payload: payload,
	})
	if s.writeCh != nil {
		select {
		case s.writeCh <- struct{}{}:
		default:
		}
	}
	return s.writeErr
}
func (s *qblockTestSession) WriteMulticastMessage(*pool.Message, *net.UDPAddr, ...coapNet.MulticastOption) error {
	return nil
}
func (s *qblockTestSession) Run(*Conn) error                          { return nil }
func (s *qblockTestSession) AddOnClose(f EventFunc)                   { s.onClose = append(s.onClose, f) }
func (s *qblockTestSession) SetContextValue(interface{}, interface{}) {}
func (s *qblockTestSession) Done() <-chan struct{}                    { return nil }

func (s *qblockTestSession) closeForTest() {
	for _, f := range s.onClose {
		f()
	}
}

func addRetainedObserveOption(t *testing.T, receiver *qblockReceiver, token message.Token) {
	t.Helper()
	receiver.mu.Lock()
	pending, ok := receiver.pending[string(token)]
	if ok {
		pending.options = append(pending.options, message.Option{ID: message.Observe})
		receiver.pending[string(token)] = pending
	}
	receiver.mu.Unlock()
	require.True(t, ok)
}

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

// This would fail if the private receiver changed the default no-Q2 request
// path by adding Q-Block2 or changing its transport type.
func TestDoInternalWithoutPrivateQBlockWritesOrdinaryGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := NewConnWithOpts(session, &cfg)
	require.Nil(t, cc.qblockReceiver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(message.Token{5, 6, 7})
	require.NoError(t, req.SetPath("/ordinary"))

	errCh := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(req)
		errCh <- err
	}()
	select {
	case <-session.writeCh:
	case <-time.After(time.Second):
		t.Fatal("ordinary GET was not written")
	}
	require.Len(t, session.writes, 1)
	write := session.writes[0]
	require.Equal(t, codes.GET, write.code)
	require.Equal(t, message.Confirmable, write.typ)
	require.False(t, write.options.HasOption(message.QBlock2))
	path, err := write.options.Path()
	require.NoError(t, err)
	require.Equal(t, "/ordinary", path)
	ack := cc.AcquireMessage(context.Background())
	ack.SetType(message.Acknowledgement)
	ack.SetMessageID(write.mid)
	require.False(t, cc.handleSpecialMessages(ack))
	cc.ReleaseMessage(ack)
	cancel()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("ordinary GET did not finish after cancellation")
	}
}

// This would fail if the private Q2 route intercepted ordinary classic Block2
// responses when no private receiver has been installed.
func TestClassicBlock2WithoutPrivateQBlockDeliversNormalHandler(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
	require.Nil(t, cc.qblockReceiver)
	cc.blockWise = blockwise.New(cc, time.Hour, func(error) {}, func(token message.Token) (*pool.Message, bool) {
		request := cc.AcquireMessage(context.Background())
		request.SetCode(codes.GET)
		request.SetToken(token)
		return request, true
	})

	token := message.Token{6, 7, 8}
	called := false
	cc.tokenHandlerContainer.Store(token.Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		called = true
		require.Equal(t, token, msg.Token())
		require.True(t, msg.HasOption(message.Block2))
	})
	response := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(response)
	response.SetCode(codes.Content)
	response.SetToken(token)
	value, err := blockwise.EncodeBlockOption(blockwise.SZX16, 0, false)
	require.NoError(t, err)
	response.SetOptionUint32(message.Block2, value)
	response.SetBody(bytes.NewReader([]byte("classic-body")))
	writer := responsewriter.New(cc.AcquireMessage(context.Background()), cc)
	defer cc.ReleaseMessage(writer.Message())

	cc.handle(writer, response)

	require.True(t, called)
	_, registered := cc.tokenHandlerContainer.Load(token.Hash())
	require.False(t, registered)
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
	first.SetContentFormat(message.TextPlain)
	first.SetOptionUint32(message.MaxAge, 60)
	last := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(last)
	last.SetCode(codes.Content)
	last.SetToken(req.Token())
	value, err := qblock.EncodeBlock(qblock.Block{Number: 1, More: false, SZX: blockwise.SZX16})
	require.NoError(t, err)
	last.SetOptionUint32(message.QBlock2, value)
	last.SetOptionUint32(message.Size2, 32)
	require.NoError(t, last.SetETag([]byte("etag-a")))
	last.SetContentFormat(message.TextPlain)
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
	require.Equal(t, codes.Content, delivered.Code())
	require.True(t, delivered.HasOption(message.ETag))
	require.True(t, delivered.HasOption(message.ContentFormat))
	require.True(t, delivered.HasOption(message.MaxAge))
	require.False(t, delivered.HasOption(message.QBlock2))
	require.False(t, delivered.HasOption(message.Size2))
	require.Zero(t, cc.qblockReceiver.active())
}

func TestConnDeliversSingleFragmentQBlockResponseThroughOriginalHandler(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{15, 16, 17})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	var delivered *pool.Message
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		delivered = msg
	})
	response := newQBlockFragment(t, cc, req.Token(), 0, false, 16)
	defer cc.ReleaseMessage(response)

	cc.handle(nil, response)

	require.NotNil(t, delivered)
	defer cc.ReleaseMessage(delivered)
	body, err := io.ReadAll(delivered.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{'a'}, 16), body)
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, cc.qblockReceiver.pending)
	require.Empty(t, cc.qblockReceiver.transfers)
	require.Empty(t, cc.qblockReceiver.transferByToken)
}

func TestQBlockDeliveryAllowsHandlerToReenterReceiver(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	handlerDone := make(chan struct{})
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		_ = cc.qblockReceiver.active()
		cc.ReleaseMessage(msg)
		close(handlerDone)
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

	cc.handle(nil, first)
	go cc.handle(nil, last)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("Q-Block handler remained blocked by the receiver lock")
	}
}

func TestQBlockReceiverSendsContinueWithFreshToken(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	allocationCalls := 0
	cc := newPrivateQBlockConnWithTokenAndSZX(t, session, func() (message.Token, error) {
		allocationCalls++
		switch allocationCalls {
		case 1:
			return nil, nil
		case 2:
			return message.Token{0xbb}, nil
		case 3:
			return message.Token{17, 18, 19}, nil
		default:
			return message.Token{0xaa}, nil
		}
	}, blockwise.SZX64)
	req := newPrivateQBlockGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	req.SetOptionUint32(message.Size2, 704)
	require.NoError(t, req.SetETag([]byte("req-tag")))
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(message.Token{0xbb}.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	defer cc.tokenHandlerContainer.Delete(message.Token{0xbb}.Hash())
	addRetainedObserveOption(t, cc.qblockReceiver, req.Token())

	for number := uint32(0); number < 10; number++ {
		fragment := newQBlockFragmentWithSZX(t, cc, req.Token(), number, true, 704, blockwise.SZX64)
		cc.handle(nil, fragment)
		cc.ReleaseMessage(fragment)
	}

	require.Len(t, session.writes, 1)
	write := session.writes[0]
	require.Equal(t, codes.GET, write.code)
	require.Equal(t, message.NonConfirmable, write.typ)
	require.NotEqual(t, req.Token(), write.token)
	require.Equal(t, 4, allocationCalls)
	require.NotZero(t, write.mid)
	path, err := write.options.Path()
	require.NoError(t, err)
	require.Equal(t, "/temperature", path)
	require.False(t, write.options.HasOption(message.ETag))
	require.False(t, write.options.HasOption(message.Observe))
	require.False(t, write.options.HasOption(message.Size2))
	require.Empty(t, write.payload)
	block, err := qblock.DecodeBlock(write.block)
	require.NoError(t, err)
	require.Equal(t, qblock.Block{Number: 10, More: true, SZX: blockwise.SZX64}, block)
}

func TestQBlockReceiverSendsAscendingRepairRequests(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	next := byte(0xb0)
	cc := newPrivateQBlockConnWithToken(t, session, func() (message.Token, error) {
		token := message.Token{next}
		next++
		return token, nil
	})
	req := newPrivateQBlockGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	first := newQBlockFragment(t, cc, req.Token(), 0, true, 176)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	last := newQBlockFragment(t, cc, req.Token(), 10, false, 176)
	defer cc.ReleaseMessage(last)
	cc.handle(nil, last)

	require.Len(t, session.writes, 9)
	seen := make(map[string]struct{}, len(session.writes))
	seenMIDs := make(map[int32]struct{}, len(session.writes))
	for index, write := range session.writes {
		require.Equal(t, codes.GET, write.code)
		require.Equal(t, message.NonConfirmable, write.typ)
		require.NotEqual(t, req.Token(), write.token)
		_, duplicate := seen[string(write.token)]
		require.False(t, duplicate)
		seen[string(write.token)] = struct{}{}
		require.NotZero(t, write.mid)
		_, duplicate = seenMIDs[write.mid]
		require.False(t, duplicate)
		seenMIDs[write.mid] = struct{}{}
		path, err := write.options.Path()
		require.NoError(t, err)
		require.Equal(t, "/temperature", path)
		require.False(t, write.options.HasOption(message.ETag))
		require.False(t, write.options.HasOption(message.Observe))
		require.False(t, write.options.HasOption(message.Size2))
		require.Empty(t, write.payload)
		block, err := qblock.DecodeBlock(write.block)
		require.NoError(t, err)
		require.Equal(t, qblock.Block{Number: uint32(index + 1), SZX: blockwise.SZX16}, block)
	}
}

func TestQBlockControlWriteFailureReleasesReceiver(t *testing.T) {
	writeErr := errors.New("network down")
	session := &qblockTestSession{ctx: context.Background(), writeErr: writeErr}
	cc := newPrivateQBlockConnWithToken(t, session, func() (message.Token, error) { return message.Token{0xcc}, nil })
	req := newPrivateQBlockGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockFragment(t, cc, req.Token(), 0, true, 176)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	last := newQBlockFragment(t, cc, req.Token(), 10, false, 176)
	defer cc.ReleaseMessage(last)
	cc.handle(nil, last)

	require.ErrorIs(t, <-errCh, writeErr)
	require.Zero(t, cc.qblockReceiver.active())
}

func TestQBlockControlTokenAllocationIsBounded(t *testing.T) {
	allocationCalls := 0
	cc := newPrivateQBlockConnWithToken(t, &qblockTestSession{ctx: context.Background()}, func() (message.Token, error) {
		allocationCalls++
		return nil, nil
	})
	req := newPrivateQBlockGET(t, cc, message.Token{18, 19, 20})
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})

	for number := uint32(0); number < 10; number++ {
		fragment := newQBlockFragment(t, cc, req.Token(), number, true, 176)
		cc.handle(nil, fragment)
		cc.ReleaseMessage(fragment)
	}

	require.ErrorIs(t, <-failures, errQBlockControlToken)
	require.Equal(t, 32, allocationCalls)
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, cc.qblockReceiver.pending)
	require.Empty(t, cc.qblockReceiver.transfers)
	require.Empty(t, cc.qblockReceiver.transferByToken)
	_, ok := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, ok)
}

func TestQBlockInvalidManagerConfigHooksAreSafe(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.MaxTransfers = 0
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg,
		withQBlockReceiver(qblockReceiverConfig{Manager: managerConfig, Now: time.Now}),
	)
	req := newPrivateQBlockGET(t, cc, message.Token{21, 22, 23})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
	require.False(t, prepared)
	require.ErrorIs(t, err, errInvalidQBlockReceiverConfig)

	require.NotPanics(t, func() { cc.CheckExpirations(time.Now()) })
	require.NotPanics(t, session.closeForTest)
	require.Zero(t, cc.qblockReceiver.active())
}

func TestQBlockExpiryFailsOriginalRequestAndReleasesState(t *testing.T) {
	now := time.Unix(100, 0)
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.NonMaxRetransmit = 0
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockReceiver(qblockReceiverConfig{Manager: managerConfig, Now: func() time.Time { return now }}),
	)
	req := newPrivateQBlockGET(t, cc, message.Token{20, 21, 22})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())

	cc.CheckExpirations(now.Add(managerConfig.Transfer.NonReceiveTimeout))
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, qblock.ErrRetriesExhausted)
	case <-time.After(time.Second):
		t.Fatal("Q-Block expiry did not notify the original request")
	}
	require.Zero(t, cc.qblockReceiver.active())
}

func TestQBlockAbandonReleasesActiveReceiver(t *testing.T) {
	cc := newPrivateQBlockConn(t)
	req := newPrivateQBlockGET(t, cc, message.Token{23, 24, 25})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())

	cc.qblockReceiver.abandon(req.Token(), context.Canceled)
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Q-Block abandonment did not notify the original request")
	}
	require.Zero(t, cc.qblockReceiver.active())
}

// This would fail if a malformed follow-on were dropped instead of canceling
// the established Q2 transfer and reporting its protocol error to doInternal.
func TestQBlockConflictingFollowOnFailsOriginalDoAndReleasesState(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockGET(t, cc, message.Token{24, 25, 26})
	defer cc.ReleaseMessage(req)

	errCh := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(req)
		errCh <- err
	}()
	select {
	case <-session.writeCh:
	case <-time.After(time.Second):
		t.Fatal("private Q-Block request was not written")
	}

	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())

	conflicting := newQBlockFragment(t, cc, req.Token(), 1, false, 32)
	defer cc.ReleaseMessage(conflicting)
	require.NoError(t, conflicting.SetETag([]byte("etag-b")))
	cc.handle(nil, conflicting)

	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "metadata changed")
	case <-time.After(time.Second):
		t.Fatal("conflicting Q-Block fragment did not fail the original request")
	}
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, cc.qblockReceiver.pending)
	require.Empty(t, cc.qblockReceiver.transfers)
	require.Empty(t, cc.qblockReceiver.transferByToken)
	_, ok := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, ok)
}

func TestQBlockConnectionCloseReleasesActiveReceiver(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockGET(t, cc, message.Token{26, 27, 28})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())

	session.closeForTest()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, qblock.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("Q-Block connection close did not notify the original request")
	}
	require.Zero(t, cc.qblockReceiver.active())
}

// This would fail if doInternal treated a canceled connection as a caller
// cancellation before the session's deferred Q2 close callback ran.
func TestQBlockConnectionShutdownFailsActiveDoWithClosedAndNoLeak(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	session := &qblockTestSession{ctx: sessionCtx, writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockGET(t, cc, message.Token{27, 28, 29})
	defer cc.ReleaseMessage(req)
	result := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(req)
		result <- err
	}()
	select {
	case <-session.writeCh:
	case <-time.After(time.Second):
		t.Fatal("private Q-Block request was not written")
	}
	first := newQBlockResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockReceiver.active())

	cancelSession()
	select {
	case err := <-result:
		require.ErrorIs(t, err, qblock.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("connection shutdown did not fail the original Q-Block request")
	}
	// The session invokes connection callbacks after exposing its canceled
	// context. This must be harmless because doInternal already closed Q2.
	session.closeForTest()
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, cc.qblockReceiver.pending)
	require.Empty(t, cc.qblockReceiver.transfers)
	require.Empty(t, cc.qblockReceiver.transferByToken)
}

// This would fail if merely enabling the private receiver changed connection
// shutdown errors for a request that was not prepared as a Q2 GET.
func TestQBlockEnabledIneligibleRequestKeepsGenericConnectionCloseError(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *pool.Message)
	}{
		{"POST", func(_ *testing.T, req *pool.Message) { req.SetCode(codes.POST) }},
		{"ObserveGET", func(_ *testing.T, req *pool.Message) { req.SetOptionUint32(message.Observe, 0) }},
		{"BodyGET", func(_ *testing.T, req *pool.Message) { req.SetBody(bytes.NewReader([]byte("body"))) }},
		{"ExistingQBlock2", func(t *testing.T, req *pool.Message) {
			value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16})
			require.NoError(t, err)
			req.SetOptionUint32(message.QBlock2, value)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionCtx, cancelSession := context.WithCancel(context.Background())
			session := &qblockTestSession{ctx: sessionCtx, writeCh: make(chan struct{}, 1)}
			cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
			req := newPrivateQBlockGET(t, cc, message.Token{28, 29, 30})
			defer cc.ReleaseMessage(req)
			test.configure(t, req)

			result := make(chan error, 1)
			go func() {
				_, err := cc.doInternal(req)
				result <- err
			}()
			select {
			case <-session.writeCh:
			case <-time.After(time.Second):
				t.Fatal("ineligible request was not written")
			}
			require.Len(t, session.writes, 1)
			ack := cc.AcquireMessage(context.Background())
			ack.SetType(message.Acknowledgement)
			ack.SetMessageID(session.writes[0].mid)
			require.False(t, cc.handleSpecialMessages(ack))
			cc.ReleaseMessage(ack)

			cancelSession()
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
				require.ErrorContains(t, err, "connection was closed")
				require.NotErrorIs(t, err, qblock.ErrClosed)
			case <-time.After(time.Second):
				t.Fatal("connection shutdown did not fail the ineligible request")
			}
			require.Zero(t, cc.qblockReceiver.active())
			require.Empty(t, cc.qblockReceiver.pending)
		})
	}
}

func TestQBlockFollowOnMetadataConflictsFailOriginalDoAndReleaseState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *pool.Message)
	}{
		{"ETag", func(t *testing.T, msg *pool.Message) { require.NoError(t, msg.SetETag([]byte("etag-b"))) }},
		{"Size2", func(_ *testing.T, msg *pool.Message) { msg.SetOptionUint32(message.Size2, 48) }},
		{"SZX", func(t *testing.T, msg *pool.Message) {
			value, err := qblock.EncodeBlock(qblock.Block{Number: 1, More: false, SZX: blockwise.SZX32})
			require.NoError(t, err)
			msg.SetOptionUint32(message.QBlock2, value)
		}},
		{"ContentFormat", func(_ *testing.T, msg *pool.Message) { msg.SetContentFormat(message.AppJSON) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
			cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
			req := newPrivateQBlockGET(t, cc, message.Token{30, 31, 32})
			defer cc.ReleaseMessage(req)
			result := make(chan error, 1)
			go func() {
				_, err := cc.doInternal(req)
				result <- err
			}()
			select {
			case <-session.writeCh:
			case <-time.After(time.Second):
				t.Fatal("private Q-Block request was not written")
			}

			first := newQBlockResponse(t, cc, req.Token(), true)
			defer cc.ReleaseMessage(first)
			first.SetContentFormat(message.TextPlain)
			cc.handle(nil, first)
			require.Equal(t, uint32(1), cc.qblockReceiver.active())
			followOn := newQBlockFragment(t, cc, req.Token(), 1, false, 32)
			defer cc.ReleaseMessage(followOn)
			followOn.SetContentFormat(message.TextPlain)
			test.mutate(t, followOn)
			cc.handle(nil, followOn)

			select {
			case err := <-result:
				require.ErrorContains(t, err, "metadata changed")
			case <-time.After(time.Second):
				t.Fatal("metadata conflict did not fail the original request")
			}
			require.Zero(t, cc.qblockReceiver.active())
			require.Empty(t, cc.qblockReceiver.pending)
			require.Empty(t, cc.qblockReceiver.transfers)
			require.Empty(t, cc.qblockReceiver.transferByToken)
			_, ok := cc.tokenHandlerContainer.Load(req.Token().Hash())
			require.False(t, ok)
		})
	}
}

func newPrivateQBlockConn(t *testing.T) *Conn {
	t.Helper()
	return newPrivateQBlockConnWithToken(t, &qblockTestSession{ctx: context.Background()}, message.GetToken)
}

func newPrivateQBlockConnWithToken(t *testing.T, session *qblockTestSession, getToken func() (message.Token, error)) *Conn {
	t.Helper()
	return newPrivateQBlockConnWithTokenAndSZX(t, session, getToken, blockwise.SZX16)
}

func newPrivateQBlockConnWithTokenAndSZX(t *testing.T, session *qblockTestSession, getToken func() (message.Token, error), szx blockwise.SZX) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = szx
	cfg.GetToken = getToken
	return NewConnWithOpts(session, &cfg,
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

func newQBlockFragment(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32) *pool.Message {
	return newQBlockFragmentWithSZX(t, cc, token, number, more, size, blockwise.SZX16)
}

func newQBlockFragmentWithSZX(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32, szx blockwise.SZX) *pool.Message {
	t.Helper()
	resp := cc.AcquireMessage(context.Background())
	resp.SetCode(codes.Content)
	resp.SetToken(token)
	value, err := qblock.EncodeBlock(qblock.Block{Number: number, More: more, SZX: szx})
	require.NoError(t, err)
	resp.SetOptionUint32(message.QBlock2, value)
	resp.SetOptionUint32(message.Size2, size)
	require.NoError(t, resp.SetETag([]byte("etag-a")))
	resp.SetBody(bytes.NewReader(bytes.Repeat([]byte{'a'}, int(szx.Size()))))
	return resp
}
