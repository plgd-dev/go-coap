package client

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
	"io"
	"testing"
	"time"
)

type untouchedQBody struct{ calls int }

func (b *untouchedQBody) Read([]byte) (int, error)       { b.calls++; return 0, io.EOF }
func (b *untouchedQBody) Seek(int64, int) (int64, error) { b.calls++; return 0, nil }
func TestQBlockSelectionMatrix(t *testing.T) {
	for _, mode := range []qblock.Mode{qblock.PreferKnown, qblock.Require} {
		for _, knowledge := range []qblockCapability{qblockCapabilityUnknown, qblockCapabilitySupported, qblockCapabilityUnsupported} {
			for _, code := range []codes.Code{codes.GET, codes.POST, codes.PUT, codes.DELETE} {
				cc, _, _ := capabilityConn(t)
				config := qblock.DefaultClientConfig()
				config.Mode = mode
				cc.qblockConfig = &config
				cc.qblockKnowledge = knowledge
				req := cc.AcquireMessage(context.Background())
				req.SetCode(code)
				body := &untouchedQBody{}
				if code == codes.POST || code == codes.PUT {
					req.SetBody(body)
				}
				selected, err := cc.selectQBlock(req)
				eligible := code != codes.DELETE
				if mode == qblock.Require && !eligible {
					require.ErrorIs(t, err, qblock.ErrUnsupportedOperation)
				} else if mode == qblock.Require && knowledge == qblockCapabilityUnknown {
					require.ErrorIs(t, err, qblock.ErrCapabilityUnknown)
				} else if mode == qblock.Require && knowledge == qblockCapabilityUnsupported {
					require.ErrorIs(t, err, qblock.ErrPeerUnsupported)
				} else {
					require.NoError(t, err)
					require.Equal(t, eligible && knowledge == qblockCapabilitySupported, selected)
				}
				require.Zero(t, body.calls)
				cc.ReleaseMessage(req)
			}
		}
	}
	cc, _, _ := capabilityConn(t)
	config := qblock.DefaultClientConfig()
	config.Mode = qblock.Require
	cc.qblockConfig = &config
	cc.qblockKnowledge = qblockCapabilitySupported
	for _, opt := range []message.OptionID{message.Observe, message.Block1, message.Block2, message.QBlock1, message.QBlock2} {
		req := cc.AcquireMessage(context.Background())
		req.SetCode(codes.GET)
		req.SetOptionUint32(opt, 0)
		_, err := cc.selectQBlock(req)
		require.ErrorIs(t, err, qblock.ErrUnsupportedOperation)
		cc.ReleaseMessage(req)
	}
}

func TestQBlockSelectionRejectsBeforeBody(t *testing.T) {
	cc, _, _ := capabilityConn(t)
	cfg := qblock.DefaultClientConfig()
	cfg.Mode = qblock.Require
	cc.qblockConfig = &cfg
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	require.NoError(t, req.SetupPost("/x", []byte{1}, message.TextPlain, &untouchedQBody{}))
	body := req.Body().(*untouchedQBody)
	_, err := cc.Do(req)
	require.ErrorIs(t, err, qblock.ErrCapabilityUnknown)
	require.Zero(t, body.calls)
}

func TestQBlockConstruction(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	_ = cc
	cfg := DefaultConfig
	q := qblock.DefaultClientConfig()
	q.Mode = qblock.Mode(9)
	cfg.QBlock = &q
	failed := NewConnWithOpts(&capabilitySession{s}, &cfg)
	require.Error(t, failed.InitializationError())
	req := failed.AcquireMessage(context.Background())
	defer failed.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken([]byte{1})
	_, err := failed.Do(req)
	require.ErrorIs(t, err, failed.InitializationError())
}

func TestQBlockNoReplay(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	s.remoteAddr = endpointPeer(123)
	q := qblock.DefaultClientConfig()
	cfg := DefaultConfig
	cfg.BlockwiseSZX = 0
	cfg.QBlock = &q
	cc = NewConnWithOpts(&capabilitySession{s}, &cfg)
	defer cc.qblockClient.close()
	cc.qblockKnowledge = qblockCapabilitySupported
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := cc.AcquireMessage(ctx)
	defer cc.ReleaseMessage(req)
	require.NoError(t, req.SetupPost("/x", []byte{9}, message.TextPlain, bytes.NewReader([]byte("x"))))
	_, err := cc.Do(req)
	require.Error(t, err)
	writes := s.writesSnapshot()
	require.NotEmpty(t, writes, "operation error: %v", err)
	for _, w := range writes {
		require.Equal(t, message.NonConfirmable, w.typ)
		require.True(t, w.options.HasOption(message.QBlock1))
		require.False(t, w.options.HasOption(message.Block1))
	}
}

func TestQBlockSelectionObserve(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	q := qblock.DefaultClientConfig()
	q.Mode = qblock.Require
	cc.qblockConfig = &q
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := cc.Observe(ctx, "/x", func(*pool.Message) {})
	require.ErrorIs(t, err, qblock.ErrUnsupportedOperation)
	req, err := cc.NewObserveRequest(ctx, "/x")
	require.NoError(t, err)
	defer cc.ReleaseMessage(req)
	_, err = cc.Do(req)
	require.ErrorIs(t, err, qblock.ErrUnsupportedOperation)
	_, err = cc.DoObserve(req, func(*pool.Message) {})
	require.ErrorIs(t, err, qblock.ErrUnsupportedOperation)
	require.Empty(t, s.writesSnapshot())
}
func TestQBlockSelectionDelayed(t *testing.T) {
	cc, s, clock, _ := capabilityEndpointConn(t)
	q := qblock.DefaultClientConfig()
	cc.qblockConfig = &q
	cc.qblockKnowledge = qblockCapabilitySupported
	m := cc.qblockClient.endpoint
	require.True(t, m.admit(99, qblockProbeControl, 0, clock.Now()))
	require.True(t, m.beginAttempt(99, 100))
	m.endAttempt(99, clock.Now())
	m.settle(99, clock.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newPrivateQBlockClientGET(t, cc, []byte{8})
	defer cc.ReleaseMessage(req)
	req.SetContext(ctx)
	result := make(chan error, 1)
	go func() { _, err := cc.Do(req); result <- err }()
	require.Eventually(t, func() bool {
		cc.qblockClient.mu.Lock()
		defer cc.qblockClient.mu.Unlock()
		return len(cc.qblockClient.exchangesByOriginalToken) == 1
	}, time.Second, time.Millisecond)
	cc.qblockProbeMu.Lock()
	cc.qblockKnowledge = qblockCapabilityUnsupported
	cc.qblockProbeMu.Unlock()
	require.True(t, m.feedback(99))
	cc.qblockClient.Tick(clock.Now())
	require.NotEmpty(t, s.writesSnapshot())
	require.True(t, s.writesSnapshot()[0].options.HasOption(message.QBlock2))
	cancel()
	require.Error(t, <-result)
}

func TestQBlockSelectionQueue(t *testing.T) {
	cc, s, _, _ := capabilityEndpointConn(t)
	q := qblock.DefaultClientConfig()
	cc.qblockConfig = &q
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := newPrivateQBlockClientGET(t, cc, []byte{1})
	first.SetContext(ctx)
	defer cc.ReleaseMessage(first)
	result := make(chan error, 2)
	go func() { _, err := cc.Do(first); result <- err }()
	select {
	case <-s.writeCh:
	case <-time.After(time.Second):
		t.Fatal("ordinary first write missing")
	}
	second := newPrivateQBlockClientGET(t, cc, []byte{2})
	second.SetContext(ctx)
	defer cc.ReleaseMessage(second)
	go func() { _, err := cc.Do(second); result <- err }()
	// Endpoint queue is already occupied by first; publication changes only later selection.
	cc.qblockProbeMu.Lock()
	cc.qblockKnowledge = qblockCapabilitySupported
	cc.qblockProbeMu.Unlock()
	reply := cc.AcquireMessage(ctx)
	reply.SetCode(codes.Content)
	reply.SetType(message.Acknowledgement)
	reply.SetToken(first.Token())
	reply.SetMessageID(s.writesSnapshot()[0].mid)
	ingestCapability(t, cc, reply)
	require.NoError(t, <-result)
	require.Eventually(t, func() bool { return len(s.writesSnapshot()) == 2 }, time.Second, time.Millisecond)
	require.False(t, s.writesSnapshot()[0].options.HasOption(message.QBlock2))
	require.True(t, s.writesSnapshot()[1].options.HasOption(message.QBlock2))
	cancel()
	require.Error(t, <-result)
}

func TestQBlockSelectionObserveCancellation(t *testing.T) {
	cc, _, _ := capabilityConn(t)
	q := qblock.DefaultClientConfig()
	q.Mode = qblock.Require
	cc.qblockConfig = &q
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetObserve(1)
	route, err := cc.selectQBlock(req)
	require.NoError(t, err)
	require.False(t, route)
}
