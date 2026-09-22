package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
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
	ctx        context.Context
	remoteAddr net.Addr
	writeType  message.Type
	writeQ2    bool
	writes     []qblockTestWrite
	onClose    []EventFunc
	writeErr   error
	writeErrAt int
	writeCh    chan struct{}

	writeMu             sync.Mutex
	firstWriteMu        sync.Mutex
	firstWriteBlocked   bool
	firstWriteStarted   chan struct{}
	releaseFirstWrite   chan struct{}
	contextWriteStart   chan struct{}
	releaseContextWrite chan struct{}
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
func (s *qblockTestSession) RemoteAddr() net.Addr {
	if s.remoteAddr != nil {
		return s.remoteAddr
	}
	return &net.UDPAddr{}
}
func (s *qblockTestSession) LocalAddr() net.Addr { return &net.UDPAddr{} }
func (s *qblockTestSession) NetConn() net.Conn   { return nil }
func (s *qblockTestSession) WriteMessage(msg *pool.Message) error {
	if s.contextWriteStart != nil {
		select {
		case s.contextWriteStart <- struct{}{}:
		default:
		}
		select {
		case <-msg.Context().Done():
			return msg.Context().Err()
		case <-s.releaseContextWrite:
			return errors.New("released blocked context write")
		}
	}
	s.firstWriteMu.Lock()
	firstWriteStarted := s.firstWriteStarted
	var releaseFirstWrite chan struct{}
	if !s.firstWriteBlocked {
		releaseFirstWrite = s.releaseFirstWrite
		s.firstWriteBlocked = releaseFirstWrite != nil
	}
	s.firstWriteMu.Unlock()
	if releaseFirstWrite != nil {
		select {
		case firstWriteStarted <- struct{}{}:
		default:
		}
		<-releaseFirstWrite
	}
	s.writeType = msg.Type()
	s.writeQ2 = msg.HasOption(message.QBlock2)
	var block uint32
	if s.writeQ2 {
		value, err := msg.GetOptionUint32(message.QBlock2)
		if err != nil {
			return err
		}
		block = value
	} else if msg.HasOption(message.QBlock1) {
		value, err := msg.GetOptionUint32(message.QBlock1)
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
	write := qblockTestWrite{
		code:    msg.Code(),
		typ:     msg.Type(),
		token:   bytes.Clone(msg.Token()),
		block:   block,
		mid:     msg.MessageID(),
		options: options,
		payload: payload,
	}
	s.writeMu.Lock()
	s.writes = append(s.writes, write)
	writeCount := len(s.writes)
	s.writeMu.Unlock()
	if s.writeCh != nil {
		select {
		case s.writeCh <- struct{}{}:
		default:
		}
	}
	if s.writeErr != nil && (s.writeErrAt == 0 || s.writeErrAt == writeCount) {
		return s.writeErr
	}
	return nil
}

func (s *qblockTestSession) writesFor(transfer *qblockTransfer) []qblockTestWrite {
	keys := make(map[string]struct{})
	if transfer != nil {
		if len(transfer.initialToken) > 0 {
			keys[string(transfer.initialToken)] = struct{}{}
		}
		for key := range transfer.tokens {
			keys[key] = struct{}{}
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	writes := make([]qblockTestWrite, 0, len(s.writes))
	for _, write := range s.writes {
		if len(keys) == 0 {
			writes = append(writes, write)
			continue
		}
		if _, ok := keys[string(write.token)]; ok {
			writes = append(writes, write)
		}
	}
	return writes
}

func (s *qblockTestSession) writesSnapshot() []qblockTestWrite {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	writes := make([]qblockTestWrite, len(s.writes))
	copy(writes, s.writes)
	return writes
}

func (s *qblockTestSession) requestTag() []byte {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if len(s.writes) == 0 {
		return nil
	}
	tag, _ := s.writes[0].options.GetBytes(message.RequestTag)
	return bytes.Clone(tag)
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

func addRetainedQBlockClientObserveOption(t *testing.T, receiver *qblockClient, token message.Token) {
	t.Helper()
	receiver.mu.Lock()
	exchange, ok := receiver.exchangesByOriginalToken[string(token)]
	if ok {
		exchange.requestOpts = append(exchange.requestOpts, message.Option{ID: message.Observe})
		receiver.exchangesByOriginalToken[string(token)] = exchange
	}
	receiver.mu.Unlock()
	require.True(t, ok)
}

func TestQBlockClientSerializesConcurrentOutputBursts(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	session.firstWriteStarted = make(chan struct{}, 1)
	session.releaseFirstWrite = make(chan struct{})
	actionMuContended := make(chan struct{}, 1)
	cc.qblockClient.actionMuContention = func() {
		select {
		case actionMuContended <- struct{}{}:
		default:
		}
	}

	first, firstDone := startQ1TransferForTest(t, cc, bytes.Repeat([]byte{'a'}, 176))
	<-session.firstWriteStarted
	tickDone := make(chan struct{})
	go func() {
		cc.qblockClient.Tick(time.Unix(200, 0))
		close(tickDone)
	}()
	select {
	case <-actionMuContended:
	case <-tickDone:
		t.Fatal("competing Q-Block tick completed without contending on ordered execution")
	case <-time.After(time.Second):
		t.Fatal("competing Q-Block tick did not reach ordered execution")
	}
	close(session.releaseFirstWrite)
	<-firstDone
	<-tickDone

	requireQ1Burst(t, session.writesFor(first), codes.POST, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, first.requestTag, bytes.Repeat([]byte{'a'}, 176))
	requireQ1Burst(t, session.writesSnapshot(), codes.POST, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, first.requestTag, bytes.Repeat([]byte{'a'}, 176))
}

func startQ1TransferForTest(t *testing.T, cc *Conn, payload []byte) (*qblockTransfer, <-chan struct{}) {
	t.Helper()
	token := message.Token{0x71}
	requestTag := []byte("tag-a")
	operation, err := q1Operation(token, requestTag)
	require.NoError(t, err)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	require.NoError(t, req.SetPath("/upload"))
	options, err := req.Options().Clone()
	require.NoError(t, err)
	metadata := qblock.Metadata{Size: uint32(len(payload)), SZX: blockwise.SZX16, Identity: requestTag}

	cc.qblockClient.mu.Lock()
	outputs, err := cc.qblockClient.manager.StartSender(operation, token, qblock.Q1, metadata, payload, time.Unix(100, 0), 0)
	require.NoError(t, err)
	id, ok := cc.qblockClient.manager.TransferID(operation)
	require.True(t, ok)
	exchange := &qblockExchange{
		originalToken: token,
		requestCode:   codes.POST,
		requestOpts:   options,
		fail:          func(error) {},
		transfers:     map[qblock.TransferID]struct{}{id: {}},
	}
	transfer := &qblockTransfer{
		id:           id,
		exchange:     exchange,
		kind:         qblock.Q1,
		operation:    operation,
		metadata:     metadata,
		requestTag:   bytes.Clone(requestTag),
		initialToken: token,
		tokens:       map[string]message.Token{string(token): token},
		mids:         make(map[int32]struct{}),
	}
	cc.qblockClient.exchangesByOriginalToken[string(token)] = exchange
	cc.qblockClient.exchangeByTransfer[id] = exchange
	cc.qblockClient.transfers[id] = transfer
	cc.qblockClient.transferByToken[string(token)] = transfer
	cc.qblockClient.mu.Unlock()

	done := make(chan struct{})
	go func() {
		cc.qblockClient.drive(outputs)
		close(done)
	}()
	return transfer, done
}

func requireQ1Burst(t *testing.T, writes []qblockTestWrite, code codes.Code, numbers []uint32, requestTag, body []byte) {
	t.Helper()
	blocks := make([]qblock.Block, 0, len(numbers))
	for index, number := range numbers {
		blocks = append(blocks, qblock.Block{Number: number, More: index+1 < len(numbers), SZX: blockwise.SZX16})
	}
	requireQ1BurstBlocks(t, writes, code, blocks, requestTag, body)
}

func requireQ1BurstBlocks(t *testing.T, writes []qblockTestWrite, code codes.Code, blocks []qblock.Block, requestTag, body []byte) {
	t.Helper()
	require.Len(t, writes, len(blocks))
	seenTokens := make(map[string]struct{}, len(writes))
	seenMIDs := make(map[int32]struct{}, len(writes))
	var payload []byte
	for index, write := range writes {
		require.Equal(t, code, write.code)
		require.Equal(t, message.NonConfirmable, write.typ)
		require.NotZero(t, write.mid)
		_, duplicate := seenTokens[string(write.token)]
		require.False(t, duplicate)
		seenTokens[string(write.token)] = struct{}{}
		_, duplicate = seenMIDs[write.mid]
		require.False(t, duplicate)
		seenMIDs[write.mid] = struct{}{}
		payload = append(payload, write.payload...)
		tag, err := write.options.GetBytes(message.RequestTag)
		require.NoError(t, err)
		require.Equal(t, requestTag, tag)
		block, err := qblock.DecodeBlock(write.block)
		require.NoError(t, err)
		require.Equal(t, blocks[index], block)
	}
	require.Equal(t, body, payload)
}

func TestQBlockClientPreparesPOSTAsQ1Burst(t *testing.T) {
	cc, session := newPrivateQBlockClientConnWithTokens(t,
		[]message.Token{{0xa1}, {0xb1}, {0xb2}, {0xb3}},
	)
	body := bytes.Repeat([]byte{'x'}, 48)
	request := newPOSTWithBody(t, cc, message.Token{0x01}, body)
	defer cc.ReleaseMessage(request)

	prepared, err := cc.qblockClient.prepare(request, func(error) {})

	require.NoError(t, err)
	require.True(t, prepared)
	writes := session.writesSnapshot()
	requireQ1Burst(t, writes, codes.POST, []uint32{0, 1, 2}, []byte{0xa1}, body)
	require.Equal(t, []message.Token{{0xb1}, {0xb2}, {0xb3}}, []message.Token{writes[0].token, writes[1].token, writes[2].token})
	for _, write := range writes {
		require.Equal(t, uint32(48), mustOptionUint32(t, write.options, message.Size1))
		require.Equal(t, []byte{0xa1}, mustOptionBytes(t, write.options, message.RequestTag))
		format, err := write.options.ContentFormat()
		require.NoError(t, err)
		require.Equal(t, message.TextPlain, format)
	}
}

func TestQBlockClientPreparesPUTAsQ1Burst(t *testing.T) {
	cc, session := newPrivateQBlockClientConnWithTokens(t,
		[]message.Token{{0xa2}, {0xc1}},
	)
	body := []byte("0123456789abcdef")
	request := newPOSTWithBody(t, cc, message.Token{0x03}, body)
	defer cc.ReleaseMessage(request)
	request.SetCode(codes.PUT)

	prepared, err := cc.qblockClient.prepare(request, func(error) {})

	require.NoError(t, err)
	require.True(t, prepared)
	requireQ1Burst(t, session.writesSnapshot(), codes.PUT, []uint32{0}, []byte{0xa2}, body)
}

func TestQBlockClientContinuesAndRepairsQ1ByAnyFragmentToken(t *testing.T) {
	cc, session := startThreeBlockQ1(t)
	last := session.writes[2].token

	cc.handle(nil, q1Continue(t, cc, last, 2))
	requireQ1BurstBlocks(t, session.writes[3:], codes.POST, []qblock.Block{
		{Number: 3, More: true, SZX: blockwise.SZX16},
		{Number: 4, More: true, SZX: blockwise.SZX16},
		{Number: 5, More: false, SZX: blockwise.SZX16},
	}, session.requestTag(), bytes.Repeat([]byte{'x'}, 48))

	cc.handle(nil, q1Missing(t, cc, last, []uint32{1, 4}))
	cc.qblockClient.Tick(time.Now().Add(3 * time.Second))
	requireQ1BurstBlocks(t, session.writes[6:], codes.POST, []qblock.Block{
		{Number: 1, More: true, SZX: blockwise.SZX16},
		{Number: 4, More: true, SZX: blockwise.SZX16},
	}, session.requestTag(), bytes.Repeat([]byte{'x'}, 32))
}

func TestQBlockClientQ1ContinueReachesAdapterThroughIngress(t *testing.T) {
	cc, session := startThreeBlockQ1(t)
	last := session.writes[2].token
	response := q1Continue(t, cc, last, 2)
	response.SetType(message.NonConfirmable)
	response.SetMessageID(9001)
	defer cc.ReleaseMessage(response)
	writer := responsewriter.New(cc.AcquireMessage(context.Background()), cc)
	defer cc.ReleaseMessage(writer.Message())

	cc.handleReq(writer, response)

	requireQ1BurstBlocks(t, session.writes[3:], codes.POST, []qblock.Block{
		{Number: 3, More: true, SZX: blockwise.SZX16},
		{Number: 4, More: true, SZX: blockwise.SZX16},
		{Number: 5, More: false, SZX: blockwise.SZX16},
	}, session.requestTag(), bytes.Repeat([]byte{'x'}, 48))
}

func TestQBlockClientDeliversTerminalResponseOnce(t *testing.T) {
	cc, session, result := startDoQ1POST(t)
	response := ordinaryResponse(t, cc, session.writes[1].token, codes.Changed, []byte("ok"))
	defer cc.ReleaseMessage(response)

	cc.handle(nil, response)

	var got *pool.Message
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("terminal Q1 response was not delivered")
	}
	defer cc.ReleaseMessage(got)
	require.Equal(t, message.Token{0x01}, got.Token())
	require.Equal(t, codes.Changed, got.Code())
	body, err := io.ReadAll(got.Body())
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), body)
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	cc.handle(nil, response)
	select {
	case duplicate := <-result:
		cc.ReleaseMessage(duplicate)
		t.Fatal("terminal Q1 response delivered more than once")
	default:
	}
}

func TestQBlockClientPendingTerminalResponseIgnoresSecondOwnedTerminal(t *testing.T) {
	cc, session, result := startDoQ1POST(t)
	first := ordinaryResponse(t, cc, session.writes[0].token, codes.Changed, []byte("first"))
	first.SetMessageID(9101)
	defer cc.ReleaseMessage(first)
	second := ordinaryResponse(t, cc, session.writes[1].token, codes.Deleted, []byte("second"))
	second.SetMessageID(9102)
	defer cc.ReleaseMessage(second)
	contended := make(chan struct{}, 2)
	cc.qblockClient.actionMuContention = func() {
		select {
		case contended <- struct{}{}:
		default:
		}
	}

	cc.qblockClient.actionMu.Lock()
	firstDone := make(chan struct{})
	go func() {
		cc.handle(nil, first)
		close(firstDone)
	}()
	select {
	case <-contended:
	case <-time.After(time.Second):
		t.Fatal("first terminal response did not reach pending ordered execution")
	}
	secondDone := make(chan struct{})
	go func() {
		cc.handle(nil, second)
		close(secondDone)
	}()
	select {
	case <-contended:
	case <-time.After(time.Second):
		t.Fatal("second terminal response did not reach pending ordered execution")
	}
	cc.qblockClient.actionMu.Unlock()

	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first terminal response did not complete")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second terminal response did not complete")
	}

	got := requireQBlockResponse(t, result)
	defer cc.ReleaseMessage(got)
	require.Equal(t, message.Token{0x01}, got.Token())
	require.Equal(t, codes.Changed, got.Code())
	body, err := io.ReadAll(got.Body())
	require.NoError(t, err)
	require.Equal(t, []byte("first"), body)
	select {
	case duplicate := <-result:
		cc.ReleaseMessage(duplicate)
		t.Fatal("pending terminal response delivered more than once")
	default:
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
}

func TestQBlockClientMalformedQ1ControlsFailOnceAndReleaseState(t *testing.T) {
	tests := []struct {
		name    string
		control func(*testing.T, *Conn, message.Token) *pool.Message
	}{
		{
			name: "MalformedContinue",
			control: func(t *testing.T, cc *Conn, token message.Token) *pool.Message {
				msg := q1Continue(t, cc, token, 2)
				msg.AddOptionUint32(message.QBlock1, 0)
				return msg
			},
		},
		{
			name: "WrongMissingContentFormat",
			control: func(t *testing.T, cc *Conn, token message.Token) *pool.Message {
				msg := q1Missing(t, cc, token, []uint32{1})
				msg.SetContentFormat(message.TextPlain)
				return msg
			},
		},
		{
			name: "InvalidMissingCBOR",
			control: func(t *testing.T, cc *Conn, token message.Token) *pool.Message {
				msg := q1Missing(t, cc, token, []uint32{1})
				msg.SetBody(bytes.NewReader([]byte{0xff}))
				return msg
			},
		},
		{
			name: "OversizeMissingCBOR",
			control: func(t *testing.T, cc *Conn, token message.Token) *pool.Message {
				msg := q1Missing(t, cc, token, []uint32{6})
				return msg
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cc, session, failures := startFailingQ1POST(t)
			msg := test.control(t, cc, session.writes[1].token)
			defer cc.ReleaseMessage(msg)

			require.True(t, cc.qblockClient.handle(msg))

			require.Error(t, requireQBlockFailure(t, failures))
			select {
			case err := <-failures:
				t.Fatalf("Q1 control failure reported more than once: %v", err)
			default:
			}
			requireQBlockClientEmpty(t, cc)
			_, registered := cc.tokenHandlerContainer.Load(message.Token{0x01}.Hash())
			require.False(t, registered)
		})
	}
}

func TestQBlockClientUnknownQ1TokenDoesNotConsumeControl(t *testing.T) {
	cc, _, failures := startFailingQ1POST(t)
	msg := q1Continue(t, cc, message.Token{0xee}, 2)
	defer cc.ReleaseMessage(msg)

	require.False(t, cc.qblockClient.handle(msg))

	select {
	case err := <-failures:
		t.Fatalf("unknown Q1 token failed active transfer: %v", err)
	default:
	}
	require.Equal(t, uint32(1), cc.qblockClient.active())
}

func TestQBlockClientResetOnQ1MIDFailsOnceAndReleasesState(t *testing.T) {
	cc, session, failures := startFailingQ1POST(t)
	reset := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(reset)
	reset.SetType(message.Reset)
	reset.SetCode(codes.Empty)
	reset.SetMessageID(session.writes[1].mid)
	reset.SetToken(session.writes[1].token)

	require.True(t, cc.qblockClient.handle(reset))
	require.ErrorIs(t, requireQBlockFailure(t, failures), qblock.ErrCanceled)
	cc.handle(nil, reset)
	select {
	case err := <-failures:
		t.Fatalf("Q1 reset failure reported more than once: %v", err)
	default:
	}
	requireQBlockClientEmpty(t, cc)
}

func TestQBlockClientQ1SecondFragmentWriteFailureReleasesState(t *testing.T) {
	writeErr := errors.New("write second q1 fragment")
	session := &qblockTestSession{ctx: context.Background(), writeErr: writeErr, writeErrAt: 2}
	tokens := [][]byte{{0xa1}, {0xb1}, {0xb2}}
	next := 0
	cc := newPrivateQBlockClientConnWithMaxPayloads(t, session, 3, func() (message.Token, error) {
		token := message.Token(bytes.Clone(tokens[next]))
		next++
		return token, nil
	})
	request := newPOSTWithBody(t, cc, message.Token{0x01}, bytes.Repeat([]byte{'x'}, 96))
	defer cc.ReleaseMessage(request)
	failures := make(chan error, 2)

	prepared, err := cc.qblockClient.prepare(request, func(err error) { failures <- err })

	require.NoError(t, err)
	require.True(t, prepared)
	require.ErrorIs(t, requireQBlockFailure(t, failures), writeErr)
	select {
	case err := <-failures:
		t.Fatalf("Q1 write failure reported more than once: %v", err)
	default:
	}
	requireQBlockClientEmpty(t, cc)
	require.Len(t, session.writes, 2)
}

func TestQBlockClientQ1CancelCleanupIsIdempotent(t *testing.T) {
	cc, _, failures := startFailingQ1POST(t)

	cc.qblockClient.abandon(message.Token{0x01}, context.Canceled)
	cc.qblockClient.abandon(message.Token{0x01}, context.Canceled)

	require.ErrorIs(t, requireQBlockFailure(t, failures), context.Canceled)
	select {
	case err := <-failures:
		t.Fatalf("Q1 cancellation reported more than once: %v", err)
	default:
	}
	requireQBlockClientEmpty(t, cc)
}

func TestQBlockClientLeavesUnsupportedQ1ShapesOrdinary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *pool.Message)
	}{
		{"Observe", func(_ *testing.T, req *pool.Message) { req.SetOptionUint32(message.Observe, 0) }},
		{"Block1", func(t *testing.T, req *pool.Message) {
			value, err := blockwise.EncodeBlockOption(blockwise.SZX16, 0, true)
			require.NoError(t, err)
			req.SetOptionUint32(message.Block1, value)
		}},
		{"Block2", func(t *testing.T, req *pool.Message) {
			value, err := blockwise.EncodeBlockOption(blockwise.SZX16, 0, true)
			require.NoError(t, err)
			req.SetOptionUint32(message.Block2, value)
		}},
		{"QBlock1", func(t *testing.T, req *pool.Message) {
			value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16})
			require.NoError(t, err)
			req.SetOptionUint32(message.QBlock1, value)
		}},
		{"QBlock2", func(t *testing.T, req *pool.Message) {
			value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16})
			require.NoError(t, err)
			req.SetOptionUint32(message.QBlock2, value)
		}},
		{"WithoutBody", func(_ *testing.T, req *pool.Message) { req.SetBody(nil) }},
		{"Multicast", func(_ *testing.T, req *pool.Message) {
			req.SetControlMessage(&coapNet.ControlMessage{Dst: net.ParseIP("224.0.1.187")})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cc := newPrivateQBlockClientConn(t)
			req := newPOSTWithBody(t, cc, message.Token{0x02}, []byte("body"))
			defer cc.ReleaseMessage(req)
			test.mutate(t, req)

			prepared, err := cc.qblockClient.prepare(req, func(error) {})

			require.NoError(t, err)
			require.False(t, prepared)
			requireQBlockClientEmpty(t, cc)
		})
	}
}

func TestQBlockClientLeavesMulticastSessionQ1Ordinary(t *testing.T) {
	for _, test := range []struct {
		name string
		ip   net.IP
	}{
		{name: "IPv4", ip: net.ParseIP("224.0.1.187")},
		{name: "IPv6", ip: net.ParseIP("ff02::fd")},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &qblockTestSession{
				ctx:        context.Background(),
				remoteAddr: &net.UDPAddr{IP: test.ip, Port: 5683},
			}
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			request := newPOSTWithBody(t, cc, message.Token{0x07}, []byte("body"))
			defer cc.ReleaseMessage(request)
			require.Nil(t, request.ControlMessage())

			prepared, err := cc.qblockClient.prepare(request, func(error) {})

			require.NoError(t, err)
			require.False(t, prepared)
			require.Empty(t, session.writesSnapshot())
			requireQBlockClientEmpty(t, cc)
		})
	}
}

func TestQBlockClientQ1StartFailureRollsBackReservationAndState(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxBodySize = 4
	managerConfig.MaxRetainedBytes = 4
	tokens := []message.Token{{0xa3}, {0xd1}}
	next := 0
	getToken := func() (message.Token, error) {
		token := bytes.Clone(tokens[next])
		next++
		return token, nil
	}
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.GetToken = getToken
	cc := NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: time.Now}),
	)
	request := newPOSTWithBody(t, cc, message.Token{0x04}, []byte("large"))
	defer cc.ReleaseMessage(request)

	prepared, err := cc.qblockClient.prepare(request, func(error) {})

	require.Error(t, err)
	require.False(t, prepared)
	require.Empty(t, session.writesSnapshot())
	requireQBlockClientEmpty(t, cc)
	require.NoError(t, cc.claimToken(message.Token{0xd1}, tokenOwnerRequest))
	cc.releaseToken(message.Token{0xd1}, tokenOwnerRequest)
}

func TestQBlockClientCopiesQ1BodyAndOptionsBeforeStarting(t *testing.T) {
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = 3
	now := time.Unix(100, 0)
	tokens := []message.Token{{0xa4}, {0xe1}, {0xe2}, {0xe3}, {0xe4}}
	next := 0
	getToken := func() (message.Token, error) {
		token := bytes.Clone(tokens[next])
		next++
		return token, nil
	}
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.GetToken = getToken
	cc := NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: func() time.Time { return now }}),
	)
	body := bytes.Repeat([]byte{'x'}, 64)
	reader := bytes.NewReader(body)
	_, err := reader.Seek(5, io.SeekStart)
	require.NoError(t, err)
	request := newPOSTWithBody(t, cc, message.Token{0x05}, body)
	defer cc.ReleaseMessage(request)
	request.SetBody(reader)

	prepared, err := cc.qblockClient.prepare(request, func(error) {})

	require.NoError(t, err)
	require.True(t, prepared)
	position, err := reader.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(5), position)
	require.Len(t, session.writesSnapshot(), 3)
	copy(body, bytes.Repeat([]byte{'y'}, len(body)))
	require.NoError(t, request.SetPath("/changed"))
	request.SetContentFormat(message.AppJSON)
	cc.qblockClient.Tick(now.Add(2 * time.Second))

	writes := session.writesSnapshot()
	require.Len(t, writes, 4)
	last := writes[3]
	require.Equal(t, bytes.Repeat([]byte{'x'}, 16), last.payload)
	path, err := last.options.Path()
	require.NoError(t, err)
	require.Equal(t, "/upload", path)
	format, err := last.options.ContentFormat()
	require.NoError(t, err)
	require.Equal(t, message.TextPlain, format)
}

func TestDoCanceledQ1DoesNotWriteBurst(t *testing.T) {
	cc, session := newPrivateQBlockClientConnWithTokens(t,
		[]message.Token{{0xa5}, {0xf1}, {0xf2}, {0xf3}},
	)
	cc.blockWise = blockwise.New(cc, time.Hour, func(error) {}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := bytes.Repeat([]byte{'z'}, 48)
	request := newPOSTWithBody(t, cc, message.Token{0x06}, body)
	defer cc.ReleaseMessage(request)
	request.SetContext(ctx)

	_, err := cc.do(request)

	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, session.writesSnapshot())
	position, seekErr := request.Body().Seek(0, io.SeekCurrent)
	require.NoError(t, seekErr)
	require.Equal(t, int64(0), position)
	requireQBlockClientEmpty(t, cc)
}

func TestDoQ1BlockedWriteStopsOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := &qblockTestSession{
		ctx:                 context.Background(),
		contextWriteStart:   make(chan struct{}, 1),
		releaseContextWrite: make(chan struct{}),
	}
	cc := newPrivateQBlockClientConnWithTokensAndSession(t, session,
		[]message.Token{{0xa6}, {0xf4}},
	)
	request := newPOSTWithBody(t, cc, message.Token{0x08}, []byte("0123456789abcdef"))
	defer cc.ReleaseMessage(request)
	request.SetContext(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(request)
		result <- err
	}()

	select {
	case <-session.contextWriteStart:
	case <-time.After(time.Second):
		close(session.releaseContextWrite)
		<-result
		t.Fatal("Q1 write did not start")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		close(session.releaseContextWrite)
		<-result
		t.Fatal("blocked Q1 write did not stop after caller cancellation")
	}
	close(session.releaseContextWrite)
	require.Empty(t, session.writesSnapshot())
	requireQBlockClientEmpty(t, cc)
}

func TestQBlockPrepareInitialGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX64
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(message.Token{1, 2, 3})
	require.NoError(t, req.SetPath("/temperature"))

	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	require.Equal(t, message.NonConfirmable, req.Type())
	value, err := req.GetOptionUint32(message.QBlock2)
	require.NoError(t, err)
	block, err := qblock.DecodeBlock(value)
	require.NoError(t, err)
	require.Equal(t, qblock.Block{Number: 0, More: true, SZX: blockwise.SZX64}, block)
	require.Zero(t, cc.qblockClient.active())
}

func TestDoInternalPreparesPrivateQBlockGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX64
	session := &qblockTestSession{ctx: context.Background()}
	cc := NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
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

func TestDoInternalPrivateQBlockLeavesClassicBlock2GETOrdinary(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(message.Token{4, 5, 7})
	require.NoError(t, req.SetPath("/classic"))
	value, err := blockwise.EncodeBlockOption(blockwise.SZX16, 0, true)
	require.NoError(t, err)
	req.SetOptionUint32(message.Block2, value)

	result := make(chan error, 1)
	go func() {
		_, err := cc.doInternal(req)
		result <- err
	}()
	select {
	case <-session.writeCh:
	case <-time.After(time.Second):
		t.Fatal("classic Block2 GET was not written")
	}
	writes := session.writesSnapshot()
	require.Len(t, writes, 1)
	require.True(t, writes[0].options.HasOption(message.Block2))
	require.False(t, writes[0].options.HasOption(message.QBlock1))
	require.False(t, writes[0].options.HasOption(message.QBlock2))
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("classic Block2 GET did not stop after cancellation")
	}
	requireQBlockClientEmpty(t, cc)
}

// This would fail if the private receiver changed the default no-Q2 request
// path by adding Q-Block2 or changing its transport type.
func TestDoInternalWithoutPrivateQBlockWritesOrdinaryGET(t *testing.T) {
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := NewConnWithOpts(session, &cfg)
	require.Nil(t, cc.qblockClient)

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
	require.Nil(t, cc.qblockClient)
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
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{8, 9, 10})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	invalid := newQBlockClientResponse(t, cc, req.Token(), false)
	defer cc.ReleaseMessage(invalid)
	require.True(t, cc.qblockClient.handle(invalid))
	require.Zero(t, cc.qblockClient.active())

	valid := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(valid)
	require.True(t, cc.qblockClient.handle(valid))
	require.Equal(t, uint32(1), cc.qblockClient.active())
}

func TestConnRoutesQBlockResponseBeforeTokenHandler(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{11, 12, 13})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	called := false
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
		called = true
	})
	response := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(response)
	writerMessage := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(writerMessage)

	cc.handle(responsewriter.New(writerMessage, cc), response)

	require.False(t, called)
	require.Equal(t, uint32(1), cc.qblockClient.active())
}

func TestConnDeliversCompleteQBlockResponseThroughOriginalHandler(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{14, 15, 16})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	var delivered *pool.Message
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		delivered = msg
	})
	first := newQBlockClientResponse(t, cc, req.Token(), true)
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
	require.Zero(t, cc.qblockClient.active())
}

func TestConnDeliversSingleFragmentQBlockResponseThroughOriginalHandler(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{15, 16, 17})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	var delivered *pool.Message
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		delivered = msg
	})
	response := newQBlockClientFragment(t, cc, req.Token(), 0, false, 16)
	defer cc.ReleaseMessage(response)

	cc.handle(nil, response)

	require.NotNil(t, delivered)
	defer cc.ReleaseMessage(delivered)
	body, err := io.ReadAll(delivered.Body())
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{'a'}, 16), body)
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	require.Empty(t, cc.qblockClient.transfers)
	require.Empty(t, cc.qblockClient.transferByToken)
}

func TestQBlockDeliveryAllowsHandlerToReenterReceiver(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	handlerDone := make(chan struct{})
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		msg.Hijack()
		_ = cc.qblockClient.active()
		cc.ReleaseMessage(msg)
		close(handlerDone)
	})
	first := newQBlockClientResponse(t, cc, req.Token(), true)
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

func TestQBlockClientSendsContinueWithFreshToken(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	allocationCalls := 0
	cc := newPrivateQBlockClientConnWithTokenAndSZX(t, session, func() (message.Token, error) {
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
	req := newPrivateQBlockClientGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	req.SetOptionUint32(message.Size2, 704)
	require.NoError(t, req.SetETag([]byte("req-tag")))
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(message.Token{0xbb}.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	defer cc.tokenHandlerContainer.Delete(message.Token{0xbb}.Hash())
	addRetainedQBlockClientObserveOption(t, cc.qblockClient, req.Token())

	for number := uint32(0); number < 10; number++ {
		fragment := newQBlockClientFragmentWithSZX(t, cc, req.Token(), number, true, 704, blockwise.SZX64)
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

func TestQBlockClientSendsAscendingRepairRequests(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	next := byte(0xb0)
	cc := newPrivateQBlockClientConnWithToken(t, session, func() (message.Token, error) {
		token := message.Token{next}
		next++
		return token, nil
	})
	req := newPrivateQBlockClientGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)

	first := newQBlockClientFragment(t, cc, req.Token(), 0, true, 176)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	last := newQBlockClientFragment(t, cc, req.Token(), 10, false, 176)
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
	cc := newPrivateQBlockClientConnWithToken(t, session, func() (message.Token, error) { return message.Token{0xcc}, nil })
	req := newPrivateQBlockClientGET(t, cc, message.Token{17, 18, 19})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	first := newQBlockClientFragment(t, cc, req.Token(), 0, true, 176)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	last := newQBlockClientFragment(t, cc, req.Token(), 10, false, 176)
	defer cc.ReleaseMessage(last)
	cc.handle(nil, last)

	require.ErrorIs(t, requireQBlockFailure(t, errCh), writeErr)
	cc.qblockClient.abandon(req.Token(), context.Canceled)
	session.closeForTest()
	select {
	case err := <-errCh:
		t.Fatalf("Q2 control write failure reported more than once: %v", err)
	default:
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	_, registered := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, registered)
	require.NoError(t, cc.claimToken(message.Token{0xcc}, tokenOwnerRequest))
	cc.releaseToken(message.Token{0xcc}, tokenOwnerRequest)
}

func TestQBlockControlTokenAllocationIsBounded(t *testing.T) {
	allocationCalls := 0
	cc := newPrivateQBlockClientConnWithToken(t, &qblockTestSession{ctx: context.Background()}, func() (message.Token, error) {
		allocationCalls++
		return nil, nil
	})
	req := newPrivateQBlockClientGET(t, cc, message.Token{18, 19, 20})
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})

	for number := uint32(0); number < 10; number++ {
		fragment := newQBlockClientFragment(t, cc, req.Token(), number, true, 176)
		cc.handle(nil, fragment)
		cc.ReleaseMessage(fragment)
	}

	require.ErrorIs(t, requireQBlockFailure(t, failures), errQBlockControlToken)
	require.Equal(t, 32, allocationCalls)
	cc.qblockClient.abandon(req.Token(), context.Canceled)
	cc.qblockClient.close()
	select {
	case err := <-failures:
		t.Fatalf("Q2 control token allocation failure reported more than once: %v", err)
	default:
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
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
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: time.Now}),
	)
	req := newPrivateQBlockClientGET(t, cc, message.Token{21, 22, 23})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(error) {})
	require.False(t, prepared)
	require.ErrorIs(t, err, errInvalidQBlockClientConfig)

	require.NotPanics(t, func() { cc.CheckExpirations(time.Now()) })
	require.NotPanics(t, session.closeForTest)
	require.Zero(t, cc.qblockClient.active())
}

func TestQBlockExpiryFailsOriginalRequestAndReleasesState(t *testing.T) {
	now := time.Unix(100, 0)
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.NonMaxRetransmit = 0
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: func() time.Time { return now }}),
	)
	req := newPrivateQBlockClientGET(t, cc, message.Token{20, 21, 22})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

	cc.CheckExpirations(now.Add(managerConfig.Transfer.NonReceiveTimeout))
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, qblock.ErrRetriesExhausted)
	case <-time.After(time.Second):
		t.Fatal("Q-Block expiry did not notify the original request")
	}
	require.Zero(t, cc.qblockClient.active())
}

// This would fail if ordered output execution, the failure-once guard, or any
// manager/adapter release path were removed. Holding actionMu lets the test
// deterministically queue Tick, caller abandonment, and session close against
// the same live exchange before any terminal output is applied.
func TestQBlockTickAbandonAndCloseContentionFailsOnceAndReleasesState(t *testing.T) {
	now := time.Unix(100, 0)
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.NonMaxRetransmit = 0
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cc := NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: func() time.Time { return now }}),
	)
	req := newPrivateQBlockClientGET(t, cc, message.Token{0xd1, 0xd2, 0xd3})
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 2)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { failures <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

	contended := make(chan struct{}, 3)
	cc.qblockClient.actionMuContention = func() { contended <- struct{}{} }
	cc.qblockClient.actionMu.Lock()
	actionMuLocked := true
	defer func() {
		if actionMuLocked {
			cc.qblockClient.actionMu.Unlock()
		}
	}()

	tickDone := make(chan struct{})
	go func() {
		cc.qblockClient.Tick(now.Add(managerConfig.Transfer.NonReceiveTimeout))
		close(tickDone)
	}()
	requireQBlockContention(t, contended, "Tick")

	abandonDone := make(chan struct{})
	go func() {
		cc.qblockClient.abandon(req.Token(), context.Canceled)
		close(abandonDone)
	}()
	requireQBlockContention(t, contended, "abandonment")

	closeDone := make(chan struct{})
	go func() {
		session.closeForTest()
		close(closeDone)
	}()
	requireQBlockContention(t, contended, "session close")

	cc.qblockClient.actionMu.Unlock()
	actionMuLocked = false
	requireQBlockCompletion(t, tickDone, "Tick")
	requireQBlockCompletion(t, abandonDone, "abandonment")
	requireQBlockCompletion(t, closeDone, "session close")

	require.ErrorIs(t, requireQBlockFailure(t, failures), qblock.ErrRetriesExhausted)
	select {
	case err := <-failures:
		t.Fatalf("contending lifecycle paths reported failure more than once: %v", err)
	default:
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	_, registered := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, registered)
}

func TestQBlockAbandonReleasesActiveReceiver(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPrivateQBlockClientGET(t, cc, message.Token{23, 24, 25})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

	cc.qblockClient.abandon(req.Token(), context.Canceled)
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Q-Block abandonment did not notify the original request")
	}
	require.Zero(t, cc.qblockClient.active())
}

// This would fail if a malformed follow-on were dropped instead of canceling
// the established Q2 transfer and reporting its protocol error to doInternal.
func TestQBlockConflictingFollowOnFailsOriginalDoAndReleasesState(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{24, 25, 26})
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

	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

	conflicting := newQBlockClientFragment(t, cc, req.Token(), 1, false, 32)
	defer cc.ReleaseMessage(conflicting)
	require.NoError(t, conflicting.SetETag([]byte("etag-b")))
	cc.handle(nil, conflicting)

	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "metadata changed")
	case <-time.After(time.Second):
		t.Fatal("conflicting Q-Block fragment did not fail the original request")
	}
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	require.Empty(t, cc.qblockClient.transfers)
	require.Empty(t, cc.qblockClient.transferByToken)
	_, ok := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, ok)
}

func TestQBlockConnectionCloseReleasesActiveReceiver(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{26, 27, 28})
	defer cc.ReleaseMessage(req)
	errCh := make(chan error, 1)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { errCh <- err })
	require.NoError(t, err)
	require.True(t, prepared)
	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

	session.closeForTest()
	select {
	case err := <-errCh:
		require.ErrorIs(t, err, qblock.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("Q-Block connection close did not notify the original request")
	}
	require.Zero(t, cc.qblockClient.active())
}

// This would fail if doInternal treated a canceled connection as a caller
// cancellation before the session's deferred Q2 close callback ran.
func TestQBlockConnectionShutdownFailsActiveDoWithClosedAndNoLeak(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	session := &qblockTestSession{ctx: sessionCtx, writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{27, 28, 29})
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
	first := newQBlockClientResponse(t, cc, req.Token(), true)
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())

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
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	require.Empty(t, cc.qblockClient.transfers)
	require.Empty(t, cc.qblockClient.transferByToken)
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
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			req := newPrivateQBlockClientGET(t, cc, message.Token{28, 29, 30})
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
			require.Zero(t, cc.qblockClient.active())
			require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
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
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			req := newPrivateQBlockClientGET(t, cc, message.Token{30, 31, 32})
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

			first := newQBlockClientResponse(t, cc, req.Token(), true)
			defer cc.ReleaseMessage(first)
			first.SetContentFormat(message.TextPlain)
			cc.handle(nil, first)
			require.Equal(t, uint32(1), cc.qblockClient.active())
			followOn := newQBlockClientFragment(t, cc, req.Token(), 1, false, 32)
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
			require.Zero(t, cc.qblockClient.active())
			require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
			require.Empty(t, cc.qblockClient.transfers)
			require.Empty(t, cc.qblockClient.transferByToken)
			_, ok := cc.tokenHandlerContainer.Load(req.Token().Hash())
			require.False(t, ok)
		})
	}
}

func newPrivateQBlockClientConn(t *testing.T) *Conn {
	t.Helper()
	return newPrivateQBlockClientConnWithToken(t, &qblockTestSession{ctx: context.Background()}, message.GetToken)
}

func newPrivateQBlockClientConnWithToken(t *testing.T, session *qblockTestSession, getToken func() (message.Token, error)) *Conn {
	t.Helper()
	return newPrivateQBlockClientConnWithTokenAndSZX(t, session, getToken, blockwise.SZX16)
}

func newPrivateQBlockClientConnWithTokenAndSZX(t *testing.T, session *qblockTestSession, getToken func() (message.Token, error), szx blockwise.SZX) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = szx
	cfg.GetToken = getToken
	return NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), Now: time.Now}),
	)
}

func newPrivateQBlockClientConnWithMaxPayloads(t *testing.T, session *qblockTestSession, maxPayloads uint32, getToken func() (message.Token, error)) *Conn {
	t.Helper()
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.GetToken = getToken
	managerConfig := qblock.DefaultManagerConfig()
	managerConfig.Transfer.MaxPayloads = maxPayloads
	return NewConnWithOpts(session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: time.Now}),
	)
}

func newPrivateQBlockClientConnWithTokens(t *testing.T, tokens []message.Token) (*Conn, *qblockTestSession) {
	t.Helper()
	session := &qblockTestSession{ctx: context.Background()}
	next := 0
	getToken := func() (message.Token, error) {
		if next >= len(tokens) {
			return nil, errors.New("test token sequence exhausted")
		}
		token := bytes.Clone(tokens[next])
		next++
		return token, nil
	}
	return newPrivateQBlockClientConnWithToken(t, session, getToken), session
}

func newPrivateQBlockClientConnWithTokensAndSession(t *testing.T, session *qblockTestSession, tokens []message.Token) *Conn {
	t.Helper()
	next := 0
	return newPrivateQBlockClientConnWithToken(t, session, func() (message.Token, error) {
		if next >= len(tokens) {
			return nil, errors.New("test token sequence exhausted")
		}
		token := bytes.Clone(tokens[next])
		next++
		return token, nil
	})
}

func startThreeBlockQ1(t *testing.T) (*Conn, *qblockTestSession) {
	t.Helper()
	tokens := []message.Token{{0xa1}, {0xb1}, {0xb2}, {0xb3}, {0xb4}, {0xb5}, {0xb6}, {0xb7}, {0xb8}}
	session := &qblockTestSession{ctx: context.Background()}
	next := 0
	cc := newPrivateQBlockClientConnWithMaxPayloads(t, session, 3, func() (message.Token, error) {
		if next >= len(tokens) {
			return nil, errors.New("test token sequence exhausted")
		}
		token := message.Token(bytes.Clone(tokens[next]))
		next++
		return token, nil
	})
	request := newPOSTWithBody(t, cc, message.Token{0x01}, bytes.Repeat([]byte{'x'}, 96))
	defer cc.ReleaseMessage(request)
	prepared, err := cc.qblockClient.prepare(request, func(error) {})
	require.NoError(t, err)
	require.True(t, prepared)
	requireQ1BurstBlocks(t, session.writesSnapshot(), codes.POST, []qblock.Block{
		{Number: 0, More: true, SZX: blockwise.SZX16},
		{Number: 1, More: true, SZX: blockwise.SZX16},
		{Number: 2, More: true, SZX: blockwise.SZX16},
	}, []byte{0xa1}, bytes.Repeat([]byte{'x'}, 48))
	return cc, session
}

func startFailingQ1POST(t *testing.T) (*Conn, *qblockTestSession, chan error) {
	t.Helper()
	cc, session := startThreeBlockQ1(t)
	failures := make(chan error, 2)
	cc.qblockClient.mu.Lock()
	exchange := cc.qblockClient.exchangesByOriginalToken[string(message.Token{0x01})]
	require.NotNil(t, exchange)
	exchange.fail = func(err error) { failures <- err }
	cc.qblockClient.mu.Unlock()
	cc.tokenHandlerContainer.Store(message.Token{0x01}.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})
	return cc, session, failures
}

func TestQBlockClientHandsQ1ResponseToQ2Receiver(t *testing.T) {
	cc, session, result := startDoQ1POST(t)
	writes := session.writesSnapshot()
	token := writes[len(writes)-1].token
	first := q2ResponseForPost(t, cc, token, 0, true, 32, 'a')
	defer cc.ReleaseMessage(first)
	cc.handle(nil, first)
	require.Equal(t, uint32(1), cc.qblockClient.active())
	require.Equal(t, qblock.Q2, cc.qblockClient.transferByToken[string(token)].kind)
	for _, write := range writes[:len(writes)-1] {
		require.NoError(t, cc.claimToken(write.token, tokenOwnerRequest))
		cc.releaseToken(write.token, tokenOwnerRequest)
		require.NotContains(t, cc.qblockClient.transferByMID, write.mid)
	}
	// The terminal upload token remains reserved while it routes the response.
	require.Error(t, cc.claimToken(token, tokenOwnerRequest))
	last := q2ResponseForPost(t, cc, token, 1, false, 32, 'b')
	defer cc.ReleaseMessage(last)
	cc.handle(nil, last)
	got := requireQBlockResponse(t, result)
	defer cc.ReleaseMessage(got)
	require.Equal(t, message.Token{0x01}, got.Token())
	require.Equal(t, codes.Changed, got.Code())
	body, err := io.ReadAll(got.Body())
	require.NoError(t, err)
	require.Equal(t, []byte("aaaaaaaaaaaaaaaabbbbbbbbbbbbbbbb"), body)
	require.False(t, got.HasOption(message.QBlock2))
	require.False(t, got.HasOption(message.Size2))
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	require.NoError(t, cc.claimToken(token, tokenOwnerRequest))
	cc.releaseToken(token, tokenOwnerRequest)
	require.Len(t, session.writesSnapshot(), 3)
}

func TestQBlockClientHandoffQ2ControlsPreserveRequestMethodAndOptions(t *testing.T) {
	for _, test := range []struct {
		name   string
		method codes.Code
		block  qblock.Block
	}{
		{name: "POST/Continue", method: codes.POST, block: qblock.Block{Number: 1, More: true, SZX: blockwise.SZX16}},
		{name: "POST/Missing", method: codes.POST, block: qblock.Block{Number: 2, SZX: blockwise.SZX16}},
		{name: "PUT/Continue", method: codes.PUT, block: qblock.Block{Number: 1, More: true, SZX: blockwise.SZX16}},
		{name: "PUT/Missing", method: codes.PUT, block: qblock.Block{Number: 2, SZX: blockwise.SZX16}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			nextToken := byte(0xa0)
			cc := newPrivateQBlockClientConnWithMaxPayloads(t, session, 2, func() (message.Token, error) {
				token := message.Token{nextToken}
				nextToken++
				return token, nil
			})
			defer cc.qblockClient.close()

			request := newPOSTWithBody(t, cc, message.Token{0x01}, bytes.Repeat([]byte{'x'}, 16))
			defer cc.ReleaseMessage(request)
			request.SetCode(test.method)
			require.NoError(t, request.SetPath("/handoff/resource"))
			request.AddQuery("mode=full")
			request.SetAccept(message.AppJSON)
			require.NoError(t, request.SetETag([]byte("req-etag")))
			request.SetOptionUint32(message.Size2, 48)
			prepared, err := cc.qblockClient.prepare(request, func(error) {})
			require.NoError(t, err)
			require.True(t, prepared)
			addRetainedQBlockClientObserveOption(t, cc.qblockClient, request.Token())

			writes := session.writesSnapshot()
			require.Len(t, writes, 1)
			upload := writes[0]
			first := q2ResponseForPost(t, cc, upload.token, 0, true, 48, 'a')
			defer cc.ReleaseMessage(first)
			cc.handle(nil, first)
			followOn := q2ResponseForPost(t, cc, upload.token, test.block.Number, test.block.More, 48, 'b')
			defer cc.ReleaseMessage(followOn)
			cc.handle(nil, followOn)

			writes = session.writesSnapshot()
			require.Len(t, writes, 2)
			control := writes[1]
			require.Equal(t, test.method, control.code)
			require.Equal(t, message.NonConfirmable, control.typ)
			require.Equal(t, message.Token{0xa2}, control.token)
			require.NotEqual(t, request.Token(), control.token)
			require.NotEqual(t, upload.token, control.token)
			require.NotZero(t, control.mid)
			require.NotEqual(t, upload.mid, control.mid)
			path, err := control.options.Path()
			require.NoError(t, err)
			require.Equal(t, "/handoff/resource", path)
			queries, err := control.options.Queries()
			require.NoError(t, err)
			require.Equal(t, []string{"mode=full"}, queries)
			accept, err := control.options.Accept()
			require.NoError(t, err)
			require.Equal(t, message.AppJSON, accept)
			contentFormat, err := control.options.ContentFormat()
			require.NoError(t, err)
			require.Equal(t, message.TextPlain, contentFormat)
			require.False(t, control.options.HasOption(message.ETag))
			require.False(t, control.options.HasOption(message.Observe))
			require.False(t, control.options.HasOption(message.Size2))
			require.False(t, control.options.HasOption(message.QBlock1))
			require.Equal(t,
				mustOptionBytes(t, upload.options, message.RequestTag),
				mustOptionBytes(t, control.options, message.RequestTag))
			require.Empty(t, control.payload)
			block, err := qblock.DecodeBlock(control.block)
			require.NoError(t, err)
			if test.block.More {
				require.Equal(t, qblock.Block{Number: 2, More: true, SZX: blockwise.SZX16}, block)
			} else {
				require.Equal(t, qblock.Block{Number: 1, SZX: blockwise.SZX16}, block)
			}
		})
	}
}

func TestQBlockClientHandoffSingleFragmentDeliveryReusesSenderBudget(t *testing.T) {
	for _, test := range []struct {
		method codes.Code
		code   codes.Code
	}{
		{codes.POST, codes.Created}, {codes.POST, codes.Changed},
		{codes.POST, codes.Content}, {codes.POST, codes.Deleted},
		{codes.PUT, codes.Created}, {codes.PUT, codes.Changed},
	} {
		t.Run(test.method.String()+"/"+test.code.String(), func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cfg := DefaultConfig
			cfg.BlockwiseEnable = false
			cfg.BlockwiseSZX = blockwise.SZX16
			managerConfig := qblock.DefaultManagerConfig()
			managerConfig.MaxTransfers = 1
			managerConfig.MaxTokens = 1
			managerConfig.Transfer.MaxBodySize = 16
			managerConfig.MaxRetainedBytes = 16
			cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: managerConfig}))
			request := newPOSTWithBody(t, cc, message.Token{1}, bytes.Repeat([]byte{'x'}, 16))
			defer cc.ReleaseMessage(request)
			request.SetCode(test.method)
			prepared, err := cc.qblockClient.prepare(request, func(err error) { t.Errorf("handoff failed: %v", err) })
			require.NoError(t, err)
			require.True(t, prepared)
			result := make(chan *pool.Message, 1)
			cc.tokenHandlerContainer.Store(request.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) { result <- msg })
			response := q2ResponseForPost(t, cc, session.writesSnapshot()[0].token, 0, false, 16, 'a')
			defer cc.ReleaseMessage(response)
			response.SetCode(test.code)
			cc.handle(nil, response)
			got := requireQBlockResponse(t, result)
			defer cc.ReleaseMessage(got)
			require.Equal(t, test.code, got.Code())
			require.Equal(t, request.Token(), got.Token())
			body, err := io.ReadAll(got.Body())
			require.NoError(t, err)
			require.Equal(t, bytes.Repeat([]byte{'a'}, 16), body)
			requireQBlockClientFullyIdle(t, cc.qblockClient)
			cc.handle(nil, response)
			select {
			case <-result:
				t.Fatal("one-fragment response delivered twice")
			default:
			}
			require.Len(t, session.writesSnapshot(), 1)
		})
	}
}

func q2ResponseForPost(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32, payload byte) *pool.Message {
	t.Helper()
	msg := newQBlockClientFragment(t, cc, token, number, more, size)
	msg.SetCode(codes.Changed)
	msg.SetBody(bytes.NewReader(bytes.Repeat([]byte{payload}, 16)))
	return msg
}

func startDoQ1POST(t *testing.T) (*Conn, *qblockTestSession, chan *pool.Message) {
	t.Helper()
	tokens := []message.Token{{0xa1}, {0xb1}, {0xb2}, {0xb3}}
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 3)}
	next := 0
	cc := newPrivateQBlockClientConnWithToken(t, session, func() (message.Token, error) {
		if next >= len(tokens) {
			return nil, errors.New("test token sequence exhausted")
		}
		token := message.Token(bytes.Clone(tokens[next]))
		next++
		return token, nil
	})
	request := newPOSTWithBody(t, cc, message.Token{0x01}, bytes.Repeat([]byte{'x'}, 48))
	result := make(chan *pool.Message, 1)
	done := make(chan error, 1)
	t.Cleanup(func() {
		cc.qblockClient.close()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Error("private Q1 caller did not exit")
		}
	})
	go func() {
		response, err := cc.doInternal(request)
		result <- response
		cc.ReleaseMessage(request)
		done <- err
	}()
	for len(session.writesSnapshot()) < 3 {
		select {
		case <-session.writeCh:
		case <-time.After(time.Second):
			t.Fatal("private Q1 request burst was not written")
		}
	}
	return cc, session, result
}

func newPOSTWithBody(t *testing.T, cc *Conn, token message.Token, body []byte) *pool.Message {
	t.Helper()
	req := cc.AcquireMessage(context.Background())
	req.SetCode(codes.POST)
	req.SetToken(token)
	require.NoError(t, req.SetPath("/upload"))
	req.SetContentFormat(message.TextPlain)
	req.SetBody(bytes.NewReader(body))
	return req
}

func q1Continue(t *testing.T, cc *Conn, token message.Token, number uint32) *pool.Message {
	t.Helper()
	response := cc.AcquireMessage(context.Background())
	response.SetCode(codes.Continue)
	response.SetToken(token)
	value, err := qblock.EncodeBlock(qblock.Block{Number: number, More: true, SZX: blockwise.SZX16})
	require.NoError(t, err)
	response.SetOptionUint32(message.QBlock1, value)
	return response
}

func q1Missing(t *testing.T, cc *Conn, token message.Token, numbers []uint32) *pool.Message {
	t.Helper()
	response := cc.AcquireMessage(context.Background())
	response.SetCode(codes.RequestEntityIncomplete)
	response.SetToken(token)
	response.SetContentFormat(message.AppMissingBlocksCBORSeq)
	payload, consumed, err := qblock.EncodeMissing(numbers, 1024)
	require.NoError(t, err)
	require.Equal(t, len(numbers), consumed)
	response.SetBody(bytes.NewReader(payload))
	return response
}

func ordinaryResponse(t *testing.T, cc *Conn, token message.Token, code codes.Code, payload []byte) *pool.Message {
	t.Helper()
	response := cc.AcquireMessage(context.Background())
	response.SetCode(code)
	response.SetToken(token)
	response.SetContentFormat(message.TextPlain)
	response.SetBody(bytes.NewReader(payload))
	return response
}

func requireQBlockFailure(t *testing.T, failures <-chan error) error {
	t.Helper()
	select {
	case err := <-failures:
		return err
	case <-time.After(time.Second):
		t.Fatal("Q-Block failure callback was not invoked")
		return nil
	}
}

func requireQBlockContention(t *testing.T, contended <-chan struct{}, contender string) {
	t.Helper()
	select {
	case <-contended:
	case <-time.After(time.Second):
		t.Fatalf("%s did not contend on ordered Q-Block output execution", contender)
	}
}

func requireQBlockCompletion(t *testing.T, done <-chan struct{}, contender string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not complete after ordered Q-Block output execution resumed", contender)
	}
}

func requireQBlockResponse(t *testing.T, responses <-chan *pool.Message) *pool.Message {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(time.Second):
		t.Fatal("Q-Block response callback was not invoked")
		return nil
	}
}

func mustOptionUint32(t *testing.T, options message.Options, id message.OptionID) uint32 {
	t.Helper()
	value, err := options.GetUint32(id)
	require.NoError(t, err)
	return value
}

func mustOptionBytes(t *testing.T, options message.Options, id message.OptionID) []byte {
	t.Helper()
	value, err := options.GetBytes(id)
	require.NoError(t, err)
	return value
}

func requireQBlockClientEmpty(t *testing.T, cc *Conn) {
	t.Helper()
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, cc.qblockClient.exchangesByOriginalToken)
	require.Empty(t, cc.qblockClient.exchangeByTransfer)
	require.Empty(t, cc.qblockClient.transfers)
	require.Empty(t, cc.qblockClient.transferByToken)
	require.Empty(t, cc.qblockClient.transferByMID)
}

func newPrivateQBlockClientGET(t *testing.T, cc *Conn, token message.Token) *pool.Message {
	t.Helper()
	req := cc.AcquireMessage(context.Background())
	req.SetCode(codes.GET)
	req.SetToken(token)
	require.NoError(t, req.SetPath("/temperature"))
	return req
}

func newQBlockClientResponse(t *testing.T, cc *Conn, token message.Token, withETag bool) *pool.Message {
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

func newQBlockClientFragment(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32) *pool.Message {
	return newQBlockClientFragmentWithSZX(t, cc, token, number, more, size, blockwise.SZX16)
}

func newQBlockClientFragmentWithSZX(t *testing.T, cc *Conn, token message.Token, number uint32, more bool, size uint32, szx blockwise.SZX) *pool.Message {
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
