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
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockGET(t, cc, message.Token{40, 41, 42})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { t.Errorf("unexpected failure: %v", err) })
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
		fragment := newQBlockFragment(t, cc, req.Token(), number, number == 0, 32)
		fragment.SetType(message.NonConfirmable)
		fragment.SetMessageID(int32(100 + number))
		cc.ProcessReceivedMessageWithHandler(fragment, cc.handleReq)
	}
	require.Equal(t, 1, deliveries)
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, session.writes)
}

// A continuation response uses the fresh control token rather than the
// original request token. The adapter must index that token before writing the
// continuation, or this real ingress route drops the final response.
func TestQBlockControlTokenResponseCompletesOriginalHandler(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockConnWithToken(t, session, func() (message.Token, error) {
		return message.Token{0xc0}, nil
	})
	req := newPrivateQBlockGET(t, cc, message.Token{43, 44, 45})
	defer cc.ReleaseMessage(req)
	prepared, err := cc.qblockReceiver.prepare(req, func(err error) { t.Errorf("unexpected failure: %v", err) })
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
		fragment := newQBlockFragment(t, cc, req.Token(), number, true, 176)
		fragment.SetType(message.NonConfirmable)
		fragment.SetMessageID(int32(200 + number))
		cc.ProcessReceivedMessageWithHandler(fragment, cc.handleReq)
	}
	require.Len(t, session.writes, 1)
	controlToken := session.writes[0].token
	require.NotEqual(t, req.Token(), controlToken)

	last := newQBlockFragment(t, cc, controlToken, 10, false, 176)
	last.SetType(message.NonConfirmable)
	last.SetMessageID(210)
	cc.ProcessReceivedMessageWithHandler(last, cc.handleReq)

	require.Equal(t, 1, deliveries)
	require.Zero(t, cc.qblockReceiver.active())
	require.Empty(t, cc.qblockReceiver.transfers)
	require.Empty(t, cc.qblockReceiver.transferByToken)
}

// A peer may ignore the private Q2 advertisement and reply using classic
// Block2. The retained request used to synthesize the next classic request
// must not leak that private option into the classic exchange.
func TestQBlockClassicFallbackFollowUpDoesNotAdvertiseQBlock2(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	cc.blockWise = blockwise.New(cc, time.Hour, func(error) {}, nil)
	req := newPrivateQBlockGET(t, cc, message.Token{46, 47, 48})
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
			cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
			token := message.Token{49, 50, 51}
			req := newPrivateQBlockGET(t, cc, token)
			defer cc.ReleaseMessage(req)
			prepared, err := cc.qblockReceiver.prepare(req, func(error) {})
			require.NoError(t, err)
			require.True(t, prepared)
			cc.tokenHandlerContainer.Store(token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {})

			var baseline qblock.TransferID
			if test.conflictFixture {
				baseline = startQBlockTokenConflictFixture(t, cc.qblockReceiver, token)
			}
			fragment := newQBlockResponse(t, cc, token, true)
			defer cc.ReleaseMessage(fragment)
			test.mutate(t, fragment)
			require.True(t, cc.qblockReceiver.handle(fragment))

			if test.conflictFixture {
				requireQBlockConflictFixtureIntact(t, cc, token)
				require.Empty(t, session.writes)
				cc.qblockReceiver.mu.Lock()
				cc.qblockReceiver.manager.Cancel(baseline, qblock.ErrCanceled)
				cc.qblockReceiver.mu.Unlock()
				cc.qblockReceiver.abandon(token, qblock.ErrCanceled)
				requireQBlockReceiverFullyIdle(t, cc.qblockReceiver)
				return
			}
			requireRejectedQBlockFirstFragmentState(t, cc, session, token)
		})
	}
}

// A mixed Q1/Q2 fragment received after start must cancel the active transfer
// and surface the protocol error to the original Do call.
func TestQBlockMixedQOptionsFollowOnFailsOriginalCall(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background(), writeCh: make(chan struct{}, 1)}
	cc := newPrivateQBlockConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockGET(t, cc, message.Token{52, 53, 54})
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
	mixed := newQBlockFragment(t, cc, req.Token(), 1, false, 32)
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
	requireQBlockReceiverFullyIdle(t, cc.qblockReceiver)
	_, registered := cc.tokenHandlerContainer.Load(req.Token().Hash())
	require.False(t, registered)
}

func startQBlockTokenConflictFixture(t *testing.T, receiver *qblockReceiver, token message.Token) qblock.TransferID {
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

func requireRejectedQBlockFirstFragmentState(t *testing.T, cc *Conn, session *qblockTestSession, token message.Token) {
	t.Helper()
	r := cc.qblockReceiver
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Zero(t, r.manager.Active())
	require.Zero(t, qblockManagerTokenCountForTest(r.manager))
	require.Zero(t, qblockManagerRetainedBytesForTest(r.manager))
	require.Empty(t, r.transfers)
	require.Empty(t, r.transferByToken)
	pending, ok := r.pending[string(token)]
	require.True(t, ok, "the original pending template must survive rejection")
	require.Equal(t, token, pending.token)
	path, err := pending.options.Path()
	require.NoError(t, err)
	require.Equal(t, "/temperature", path)
	require.Empty(t, session.writes)
	_, registered := cc.tokenHandlerContainer.Load(token.Hash())
	require.True(t, registered, "the ordinary token handler must survive rejection")
}

func requireQBlockConflictFixtureIntact(t *testing.T, cc *Conn, token message.Token) {
	t.Helper()
	r := cc.qblockReceiver
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Equal(t, uint32(1), r.manager.Active())
	require.Equal(t, 1, qblockManagerTokenCountForTest(r.manager))
	require.Equal(t, uint64(32), qblockManagerRetainedBytesForTest(r.manager))
	require.Empty(t, r.transfers)
	require.Empty(t, r.transferByToken)
	require.Contains(t, r.pending, string(token))
	_, registered := cc.tokenHandlerContainer.Load(token.Hash())
	require.True(t, registered, "the ordinary token handler must survive rejection")
}

func requireQBlockReceiverFullyIdle(t *testing.T, receiver *qblockReceiver) {
	t.Helper()
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	require.Zero(t, receiver.manager.Active())
	require.Zero(t, qblockManagerTokenCountForTest(receiver.manager))
	require.Zero(t, qblockManagerRetainedBytesForTest(receiver.manager))
	require.Empty(t, receiver.transfers)
	require.Empty(t, receiver.transferByToken)
	require.Empty(t, receiver.pending)
}

// These test-only introspectors keep manager accounting private in production
// while making the rollback invariants explicit at the adapter boundary.
func qblockManagerTokenCountForTest(manager *qblock.Manager) int {
	return reflect.ValueOf(manager).Elem().FieldByName("byToken").Len()
}

func qblockManagerRetainedBytesForTest(manager *qblock.Manager) uint64 {
	return reflect.ValueOf(manager).Elem().FieldByName("retained").Uint()
}
