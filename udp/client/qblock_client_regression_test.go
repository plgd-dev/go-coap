package client

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

// The normal ingress guard must let enabled Q2 responses reach the receiver.
func TestQBlockIngressCompletesOriginalHandler(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{40, 41, 42})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { t.Errorf("unexpected failure: %v", err) })
	require.NoError(t, err)
	require.True(t, prepared)
	deliveries := 0
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		deliveries++
		defer cc.ReleaseMessage(msg)
		require.Equal(t, req.Token(), msg.Token())
		body, err := io.ReadAll(msg.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{'a'}, 32), body)
	})
	for number := uint32(0); number < 2; number++ {
		fragment := newQBlockClientFragment(t, cc, req.Token(), number, number == 0, 32)
		fragment.SetType(message.NonConfirmable)
		fragment.SetMessageID(int32(100 + number))
		cc.ProcessReceivedMessageWithHandler(fragment, cc.handleReq)
	}
	require.Equal(t, 1, deliveries)
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, session.writes)
}

// A continuation response uses the fresh control token rather than the
// original request token. The adapter must index that token before writing the
// continuation, or this real ingress route drops the final response.
func TestQBlockControlTokenResponseCompletesOriginalHandler(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, func() (message.Token, error) {
		return message.Token{0xc0}, nil
	})
	req := newPrivateQBlockClientGET(t, cc, message.Token{43, 44, 45})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockClient.prepare(req, func(err error) { t.Errorf("unexpected failure: %v", err) })
	require.NoError(t, err)
	require.True(t, prepared)

	deliveries := 0
	cc.tokenHandlerContainer.Store(req.Token().Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		deliveries++
		defer cc.ReleaseMessage(msg)
		body, err := io.ReadAll(msg.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{'a'}, 176), body)
	})
	for number := uint32(0); number < 10; number++ {
		fragment := newQBlockClientFragment(t, cc, req.Token(), number, true, 176)
		fragment.SetType(message.NonConfirmable)
		fragment.SetMessageID(int32(200 + number))
		cc.ProcessReceivedMessageWithHandler(fragment, cc.handleReq)
	}
	require.Len(t, session.writes, 1)
	controlToken := session.writes[0].token
	require.NotEqual(t, req.Token(), controlToken)

	last := newQBlockClientFragment(t, cc, controlToken, 10, false, 176)
	last.SetType(message.NonConfirmable)
	last.SetMessageID(210)
	cc.ProcessReceivedMessageWithHandler(last, cc.handleReq)

	require.Equal(t, 1, deliveries)
	require.Zero(t, cc.qblockClient.active())
	require.Empty(t, cc.qblockClient.transfers)
	require.Empty(t, cc.qblockClient.transferByToken)
}

// A peer may ignore the private Q2 advertisement and reply using classic
// Block2. The retained request used to synthesize the next classic request
// must not leak that private option into the classic exchange.
func TestQBlockClassicFallbackFollowUpDoesNotAdvertiseQBlock2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	cc.blockWise = blockwise.New(cc, time.Hour, func(error) {}, nil)
	req := newPrivateQBlockClientGET(t, cc, message.Token{46, 47, 48})
	defer cc.ReleaseMessage(req)
	req.SetContext(ctx)

	result := make(chan error, 1)
	go func() {
		_, err := cc.do(req)
		result <- err
	}()
	select {
	case <-session.writeCh:
	case <-time.After(time.Second):
		t.Fatal("private Q-Block request was not written")
	}
	require.Len(t, session.writes, 1)
	require.True(t, session.writes[0].options.HasOption(message.QBlock2))

	fragment := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(fragment)
	fragment.SetCode(codes.Content)
	fragment.SetToken(req.Token())
	value, err := blockwise.EncodeBlockOption(blockwise.SZX16, 0, true)
	require.NoError(t, err)
	fragment.SetOptionUint32(message.Block2, value)
	fragment.SetBody(bytes.NewReader(bytes.Repeat([]byte{'c'}, 16)))
	followUp := cc.AcquireMessage(context.Background())
	writer := responsewriter.New(followUp, cc)
	cc.handle(writer, fragment)
	defer cc.ReleaseMessage(writer.Message())

	require.True(t, writer.Message().HasOption(message.Block2))
	require.False(t, writer.Message().HasOption(message.QBlock2))
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("classic fallback request did not stop after cancellation")
	}
}

// Failed first fragments are transactional: they do not publish a manager
// transfer or a receiver token route, and they retain the caller's pending
// request template and ordinary handler for a later valid response.
func TestQBlockFirstFragmentRejectionsAreTransactional(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(*testing.T, *pool.Message)
		conflictFixture bool
	}{
		{
			name: "MalformedQBlock2",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.SetOptionBytes(message.QBlock2, bytes.Repeat([]byte{'q'}, 4))
			},
		},
		{
			name: "DuplicateQBlock2",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.AddOptionUint32(message.QBlock2, 0)
			},
		},
		{
			name: "DuplicateETag",
			mutate: func(t *testing.T, msg *pool.Message) {
				require.NoError(t, msg.AddETag([]byte("etag-b")))
			},
		},
		{
			name: "AbsentSize2",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.Remove(message.Size2)
			},
		},
		{
			name: "DuplicateSize2",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.AddOptionUint32(message.Size2, 32)
			},
		},
		{
			name: "InconsistentSize2",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.SetOptionUint32(message.Size2, 16)
			},
		},
		{
			name: "InvalidOffset",
			mutate: func(t *testing.T, msg *pool.Message) {
				value, err := qblock.EncodeBlock(qblock.Block{Number: 2, More: true, SZX: blockwise.SZX16})
				require.NoError(t, err)
				msg.SetOptionUint32(message.QBlock2, value)
			},
		},
		{
			name: "InvalidPayloadBounds",
			mutate: func(_ *testing.T, msg *pool.Message) {
				msg.SetBody(bytes.NewReader(bytes.Repeat([]byte{'a'}, 15)))
			},
		},
		{
			name: "QBlock1AndQBlock2",
			mutate: func(t *testing.T, msg *pool.Message) {
				value, err := qblock.EncodeBlock(qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16})
				require.NoError(t, err)
				msg.SetOptionUint32(message.QBlock1, value)
			},
		},
		{
			name:            "TokenConflict",
			mutate:          func(*testing.T, *pool.Message) {},
			conflictFixture: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
			token := message.Token{49, 50, 51}
			req := newPrivateQBlockClientGET(t, cc, token)
			defer cc.ReleaseMessage(req)
			prepared, err := cc.qblockClient.prepare(req, func(error) {})
			require.NoError(t, err)
			require.True(t, prepared)
			cc.tokenHandlerContainer.Store(token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})

			var baseline qblock.TransferID
			if test.conflictFixture {
				baseline = startQBlockClientTokenConflictFixture(t, cc.qblockClient, token)
			}
			fragment := newQBlockClientResponse(t, cc, token, true)
			defer cc.ReleaseMessage(fragment)
			test.mutate(t, fragment)
			require.True(t, cc.qblockClient.handle(fragment))

			if test.conflictFixture {
				requireQBlockClientConflictFixtureIntact(t, cc, token)
				require.Empty(t, session.writes)
				cc.qblockClient.mu.Lock()
				cc.qblockClient.manager.Cancel(baseline, qblock.ErrCanceled)
				cc.qblockClient.mu.Unlock()
				cc.qblockClient.abandon(token, qblock.ErrCanceled)
				requireQBlockClientFullyIdle(t, cc.qblockClient)
				return
			}
			requireRejectedQBlockClientFirstFragmentState(t, cc, session, token)
		})
	}
}

// A mixed Q1/Q2 fragment received after start must cancel the active transfer
// and surface the protocol error to the original Do call.
func TestQBlockMixedQOptionsFollowOnFailsOriginalCall(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{52, 53, 54})
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
	mixed := newQBlockClientFragment(t, cc, req.Token(), 1, false, 32)
	defer cc.ReleaseMessage(mixed)
	value, err := qblock.EncodeBlock(qblock.Block{Number: 1, SZX: blockwise.SZX16})
	require.NoError(t, err)
	mixed.SetOptionUint32(message.QBlock1, value)
	cc.handle(nil, mixed)

	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("mixed Q options did not finish the original request")
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	_, registered := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, registered)
}

func TestQBlockClientRejectsInvalidQ1ToQ2FirstFragmentAtomically(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*pool.Message)
	}{
		{"AbsentETag", func(m *pool.Message) { m.Remove(message.ETag) }},
		{"AbsentSize2", func(m *pool.Message) { m.Remove(message.Size2) }},
		{"DuplicateQBlock2", func(m *pool.Message) { m.AddOptionUint32(message.QBlock2, 0) }},
		{"MalformedQBlock2", func(m *pool.Message) { m.SetOptionBytes(message.QBlock2, []byte{0, 0, 0, 0}) }},
		{"MixedQOptions", func(m *pool.Message) { m.SetOptionUint32(message.QBlock1, 0) }},
		{"ClassicBlock2", func(m *pool.Message) { m.SetOptionUint32(message.Block2, 0) }},
		{"InvalidPayload", func(m *pool.Message) { m.SetBody(bytes.NewReader([]byte("short"))) }},
		{"InvalidSize", func(m *pool.Message) { m.SetOptionUint32(message.Size2, 16) }},
		{"InvalidOffset", func(m *pool.Message) { m.SetOptionUint32(message.QBlock2, 40) }},
		{"ErrorCode", func(m *pool.Message) { m.SetCode(codes.BadRequest) }},
		{"ContinueCode", func(m *pool.Message) { m.SetCode(codes.Continue) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cc, session, failures := startFailingQ1POST(t)
			defer cc.qblockClient.close()
			writes := session.writesSnapshot()
			token := writes[0].token
			before := cc.qblockClient.transferByToken[string(token)]
			first := q2ResponseForPost(t, cc, token, 0, true, 32, 'a')
			defer cc.ReleaseMessage(first)
			test.mutate(first)
			require.True(t, cc.qblockClient.handle(first))
			require.Equal(t, uint32(1), cc.qblockClient.active())
			require.Same(t, before, cc.qblockClient.transferByToken[string(token)])
			require.Equal(t, qblock.Q1, before.kind)
			require.Len(t, cc.qblockClient.transfers, 1)
			_, registered := cc.tokenHandlerContainer.Load(message.Token{0x01}.Hash())
			require.True(t, registered)
			select {
			case err := <-failures:
				t.Fatalf("invalid first fragment ended upload: %v", err)
			default:
			}
			// A valid Q1 continue still advances the surviving upload.
			control := q1Continue(t, cc, token, 2)
			defer cc.ReleaseMessage(control)
			require.True(t, cc.qblockClient.handle(control))
			require.Len(t, session.writesSnapshot(), 6)
		})
	}
}

func TestQBlockClientHandoffStartFailureEndsExchangeWithoutReplay(t *testing.T) {
	cc, session, failures := startFailingQ1POST(t)
	writes := session.writesSnapshot()
	// The existing receiver owns the prospective response operation, but not
	// any upload token: conversion succeeds and manager start must fail.
	operation, err := q2Operation(message.Token{0x01}, []byte("etag-a"))
	require.NoError(t, err)
	_, err = cc.qblockClient.manager.StartReceiver(qblock.Fragment{
		Operation: operation, Token: message.Token{0xf0}, Kind: qblock.Q2,
		Metadata: qblock.Metadata{Size: 32, SZX: blockwise.SZX16, Identity: []byte("etag-a")},
		Block:    qblock.Block{More: true, SZX: blockwise.SZX16}, Payload: bytes.Repeat([]byte{'z'}, 16),
	}, time.Now())
	require.NoError(t, err)
	baseline, ok := cc.qblockClient.manager.TransferID(operation)
	require.True(t, ok)
	first := q2ResponseForPost(t, cc, writes[0].token, 0, true, 32, 'a')
	defer cc.ReleaseMessage(first)
	require.True(t, cc.qblockClient.handle(first))
	require.ErrorIs(t, requireQBlockFailure(t, failures), qblock.ErrOperationInUse)
	require.True(t, cc.qblockClient.handle(first))
	cc.qblockClient.close()
	select {
	case err := <-failures:
		t.Fatalf("handoff failed twice: %v", err)
	default:
	}
	require.Len(t, session.writesSnapshot(), len(writes))
	require.Equal(t, uint32(1), cc.qblockClient.active(), "unrelated receiver survives")
	cc.qblockClient.manager.Cancel(baseline, qblock.ErrCanceled)
	requireQBlockClientFullyIdle(t, cc.qblockClient)
	for _, write := range writes {
		require.NoError(t, cc.claimToken(write.token, tokenOwnerRequest))
		cc.releaseToken(write.token, tokenOwnerRequest)
	}
}

func TestQBlockClientHandoffInvalidFollowOnFailsOnce(t *testing.T) {
	cc, session, failures := startFailingQ1POST(t)
	token := session.writesSnapshot()[0].token
	first := q2ResponseForPost(t, cc, token, 0, true, 32, 'a')
	defer cc.ReleaseMessage(first)
	require.True(t, cc.qblockClient.handle(first))
	require.NotNil(t, cc.qblockClient.transferByToken[string(token)])
	require.Equal(t, qblock.Q2, cc.qblockClient.transferByToken[string(token)].kind)
	last := q2ResponseForPost(t, cc, token, 1, false, 32, 'b')
	defer cc.ReleaseMessage(last)
	last.SetOptionUint32(message.QBlock1, 0)
	require.True(t, cc.qblockClient.handle(last))
	require.Error(t, requireQBlockFailure(t, failures))
	require.True(t, cc.qblockClient.handle(last))
	cc.qblockClient.close()
	select {
	case err := <-failures:
		t.Fatalf("follow-on failed twice: %v", err)
	default:
	}
	requireQBlockClientFullyIdle(t, cc.qblockClient)
}

func startQBlockClientTokenConflictFixture(t *testing.T, receiver *qblockClient, token message.Token) qblock.TransferID {
	t.Helper()
	operation, err := qblock.NewOperationKey([]byte("baseline"), []byte("identity"))
	require.NoError(t, err)
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	_, err = receiver.manager.StartReceiver(qblock.Fragment{
		Operation: operation,
		Token:     token,
		Kind:      qblock.Q2,
		Metadata: qblock.Metadata{
			Size:     32,
			SZX:      blockwise.SZX16,
			Identity: []byte("baseline"),
		},
		Block:   qblock.Block{Number: 0, More: true, SZX: blockwise.SZX16},
		Payload: bytes.Repeat([]byte{'b'}, 16),
	}, time.Now())
	require.NoError(t, err)
	id, ok := receiver.manager.TransferID(operation)
	require.True(t, ok)
	return id
}

func requireRejectedQBlockClientFirstFragmentState(t *testing.T, cc *Conn, session *qblockTestSession, token message.Token) {
	t.Helper()
	r := cc.qblockClient
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Zero(t, r.manager.Active())
	require.Zero(t, qblockClientManagerTokenCountForTest(r.manager))
	require.Zero(t, qblockClientManagerRetainedBytesForTest(r.manager))
	require.Empty(t, r.transfers)
	require.Empty(t, r.transferByToken)
	exchange, ok := r.exchangesByOriginalToken[string(token)]
	require.True(t, ok, "the original pending template must survive rejection")
	require.Equal(t, token, exchange.originalToken)
	path, err := exchange.requestOpts.Path()
	require.NoError(t, err)
	require.Equal(t, "/temperature", path)
	require.Empty(t, session.writes)
	_, registered := cc.tokenHandlerContainer.Load(token.Hash())
	require.True(t, registered, "the ordinary token handler must survive rejection")
}

func requireQBlockClientConflictFixtureIntact(t *testing.T, cc *Conn, token message.Token) {
	t.Helper()
	r := cc.qblockClient
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Equal(t, uint32(1), r.manager.Active())
	require.Equal(t, 1, qblockClientManagerTokenCountForTest(r.manager))
	require.Equal(t, uint64(32), qblockClientManagerRetainedBytesForTest(r.manager))
	require.Empty(t, r.transfers)
	require.Empty(t, r.transferByToken)
	require.Contains(t, r.exchangesByOriginalToken, string(token))
	_, registered := cc.tokenHandlerContainer.Load(token.Hash())
	require.True(t, registered, "the ordinary token handler must survive rejection")
}

func requireQBlockClientFullyIdle(t *testing.T, receiver *qblockClient) {
	t.Helper()
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	require.Zero(t, receiver.manager.Active())
	require.Zero(t, qblockClientManagerTokenCountForTest(receiver.manager))
	require.Zero(t, qblockClientManagerRetainedBytesForTest(receiver.manager))
	require.Empty(t, receiver.transfers)
	require.Empty(t, receiver.exchangeByTransfer)
	require.Empty(t, receiver.transferByToken)
	require.Empty(t, receiver.transferByMID)
	require.Empty(t, receiver.exchangesByOriginalToken)
}

// These test-only introspectors keep manager accounting private in production
// while making the rollback invariants explicit at the adapter boundary.
func qblockClientManagerTokenCountForTest(manager *qblock.Manager) int {
	return reflect.ValueOf(manager).Elem().FieldByName("byToken").Len()
}

func qblockClientManagerRetainedBytesForTest(manager *qblock.Manager) uint64 {
	return reflect.ValueOf(manager).Elem().FieldByName("retained").Uint()
}
