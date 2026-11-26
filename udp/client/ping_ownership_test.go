package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type blockedPingSession struct {
	*recordingSession
	started chan struct{}
	resume  chan struct{}
	written chan message.Message
}

func (s *blockedPingSession) WriteMessage(req *pool.Message) error {
	close(s.started)
	<-s.resume
	if req.Context() == nil {
		return errors.New("ping context was released before the write")
	}
	data, err := req.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	var decoded message.Message
	if _, err = coder.DefaultCoder.Decode(data, &decoded); err != nil {
		return err
	}
	s.written <- decoded
	return nil
}

func TestAsyncPingOwnsMessageDuringEarlyCompletion(t *testing.T) {
	for _, completion := range []string{"reset", "acknowledgement", "expiry"} {
		t.Run(completion, func(t *testing.T) {
			session := &blockedPingSession{
				recordingSession: newRecordingSession(),
				started:          make(chan struct{}),
				resume:           make(chan struct{}),
				written:          make(chan message.Message, 1),
			}
			t.Cleanup(func() { _ = session.Close() })
			resume := sync.OnceFunc(func() { close(session.resume) })
			t.Cleanup(resume)
			cfg := DefaultConfig
			cfg.MessagePool = pool.New(64, 1024)
			cfg.Errors = func(error) {}
			cc := NewConnWithOpts(session, &cfg)
			cc.msgID.Store(122)
			pong := make(chan struct{}, 1)
			type result struct {
				cancel func()
				err    error
			}
			resultCh := make(chan result, 1)
			go func() {
				cancel, err := cc.AsyncPing(func() { pong <- struct{}{} })
				resultCh <- result{cancel, err}
			}()
			select {
			case <-session.started:
			case <-time.After(time.Second):
				t.Fatal("ping did not reach the write")
			}
			element, ok := cc.midHandlerContainer.Load(123)
			require.True(t, ok)
			if completion == "expiry" {
				cc.expireMidHandler(123, element)
			} else {
				reply := cc.AcquireMessage(context.Background())
				reply.SetCode(codes.Empty)
				reply.SetMessageID(123)
				if completion == "reset" {
					reply.SetType(message.Reset)
				} else {
					reply.SetType(message.Acknowledgement)
				}
				cc.handleSpecialMessages(reply)
				cc.ReleaseMessage(reply)
				select {
				case <-pong:
				default:
					t.Fatal("pong callback was not invoked")
				}
			}
			resume()
			var got result
			select {
			case got = <-resultCh:
			case <-time.After(time.Second):
				t.Fatal("ping did not finish")
			}
			require.NoError(t, got.err)
			got.cancel()
			written := <-session.written
			require.Equal(t, message.Confirmable, written.Type)
			require.Equal(t, codes.Empty, written.Code)
			require.Equal(t, int32(123), written.MessageID)
			require.Empty(t, written.Token)
		})
	}
}

func TestAsyncPingCancellationDoesNotRemoveReplacement(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	cc.msgID.Store(41)
	firstCancel, err := cc.AsyncPing(func() {})
	require.NoError(t, err)
	reply := cc.AcquireMessage(context.Background())
	reply.SetType(message.Reset)
	reply.SetCode(codes.Empty)
	reply.SetMessageID(42)
	cc.handleSpecialMessages(reply)
	cc.ReleaseMessage(reply)
	cc.msgID.Store(41)
	secondCancel, err := cc.AsyncPing(func() {})
	require.NoError(t, err)
	defer secondCancel()

	firstCancel()
	_, ok := cc.midHandlerContainer.Load(42)
	require.True(t, ok)
	require.Equal(t, 2, session.writtenCount())
}

func TestAsyncPingMIDCollisionPreservesExistingHandler(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	cc.msgID.Store(41)
	existing := &midElement{}
	cc.midHandlerContainer.Store(42, existing)
	cancel, err := cc.AsyncPing(func() {})
	require.Error(t, err)
	require.Nil(t, cancel)
	current, ok := cc.midHandlerContainer.Load(42)
	require.True(t, ok)
	require.Same(t, existing, current)
	require.Zero(t, session.writtenCount())
	cc.midHandlerContainer.Delete(42)
}

func TestAsyncPingRetransmissionAndCancellation(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	cc.msgID.Store(41)
	cancel, err := cc.AsyncPing(func() {})
	require.NoError(t, err)
	element, ok := cc.midHandlerContainer.Load(42)
	require.True(t, ok)

	cc.checkMidHandlerContainer(element.start.Add(element.acknowledgeTimeout+time.Nanosecond), 4, 42, element)
	require.Equal(t, 2, session.writtenCount())
	written := session.lastWritten()
	require.Equal(t, message.Confirmable, written.Type())
	require.Equal(t, codes.Empty, written.Code())
	require.Equal(t, int32(42), written.MessageID())

	cancel()
	_, ok = cc.midHandlerContainer.Load(42)
	require.False(t, ok)
	element.private.Lock()
	retained := element.private.msg
	element.private.Unlock()
	require.Nil(t, retained)
}

func TestAsyncPingWriteFailureRemovesHandler(t *testing.T) {
	cc, session := newTestConnWithSession(t)
	cc.msgID.Store(41)
	writeErr := errors.New("ping write failed")
	session.setWriteError(writeErr)
	cancel, err := cc.AsyncPing(func() {})
	require.Nil(t, cancel)
	require.ErrorIs(t, err, writeErr)
	_, ok := cc.midHandlerContainer.Load(42)
	require.False(t, ok)
}
