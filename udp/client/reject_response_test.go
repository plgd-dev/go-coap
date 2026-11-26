package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestUnrecognizedConfirmableNotification(t *testing.T) {
	for _, blockOption := range []message.OptionID{0, message.Block2, message.QBlock2} {
		t.Run(blockOption.String(), func(t *testing.T) {
			session := newRecordingSession()
			cfg := DefaultConfig
			cfg.BlockwiseEnable = true
			cc := NewConnWithOpts(session, &cfg, WithBlockWise(func(cc *Conn) *blockwise.BlockWise[*Conn] {
				return blockwise.New(cc, time.Minute, func(error) {}, nil)
			}))
			defer func() { require.NoError(t, cc.Close()) }()

			req := cc.AcquireMessage(context.Background())
			req.SetType(message.Confirmable)
			req.SetMessageID(0x1234)
			req.SetToken([]byte{0xaa})
			req.SetCode(codes.Content)
			req.SetObserve(1)
			if blockOption != 0 {
				req.SetOptionUint32(blockOption, 8)
			}
			req.SetBody(bytes.NewReader([]byte("notification")))
			cc.ProcessReceivedMessageWithHandler(req, cc.handleReq)

			require.Equal(t, 1, session.writtenCount())
			wire, err := session.lastWritten().MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.Equal(t, []byte{0x70, 0x00, 0x12, 0x34}, wire)
		})
	}
}

func TestExplicitResetPreservesMIDAndIsEmpty(t *testing.T) {
	for _, typ := range []message.Type{message.Confirmable, message.NonConfirmable, message.Acknowledgement, message.Reset} {
		t.Run(typ.String(), func(t *testing.T) {
			session := newRecordingSession()
			cfg := DefaultConfig
			cfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				w.Message().SetType(message.Reset)
				w.Message().SetCode(codes.Content)
				w.Message().SetToken([]byte{1})
				w.Message().SetOptionUint32(message.Observe, 1)
				w.Message().SetBody(bytes.NewReader([]byte("discard")))
			}
			cc := NewConnWithOpts(session, &cfg)
			defer func() { require.NoError(t, cc.Close()) }()
			req := cc.AcquireMessage(context.Background())
			req.SetType(typ)
			req.SetMessageID(0x2345)
			req.SetToken([]byte{2})
			req.SetCode(codes.Content)
			cc.ProcessReceivedMessageWithHandler(req, cc.handleReq)
			if typ == message.Acknowledgement || typ == message.Reset {
				require.Zero(t, session.writtenCount())
				return
			}
			require.Equal(t, 1, session.writtenCount())
			wire, err := session.lastWritten().MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.Equal(t, []byte{0x70, 0x00, 0x23, 0x45}, wire)
		})
	}
}

func TestClaimedConfirmableResponseStillAcknowledged(t *testing.T) {
	session := newRecordingSession()
	cfg := DefaultConfig
	cc := NewConnWithOpts(session, &cfg)
	defer func() { require.NoError(t, cc.Close()) }()
	token := message.Token{0xaa}
	called := false
	cc.tokenHandlerContainer.Store(token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) {
		called = true
	})
	req := cc.AcquireMessage(context.Background())
	req.SetType(message.Confirmable)
	req.SetMessageID(0x1234)
	req.SetToken(token)
	req.SetCode(codes.Content)
	cc.ProcessReceivedMessageWithHandler(req, cc.handleReq)
	require.True(t, called)
	require.Equal(t, 1, session.writtenCount())
	wire, err := session.lastWritten().MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.Equal(t, []byte{0x60, 0x00, 0x12, 0x34}, wire)
}

func TestCustomHandlerCanClaimUnrecognizedResponse(t *testing.T) {
	session := newRecordingSession()
	cfg := DefaultConfig
	called := false
	cfg.Handler = func(_ *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		called = true
		require.Equal(t, codes.Content, r.Code())
	}
	cc := NewConnWithOpts(session, &cfg)
	defer func() { require.NoError(t, cc.Close()) }()
	req := cc.AcquireMessage(context.Background())
	req.SetType(message.Confirmable)
	req.SetMessageID(0x1234)
	req.SetToken([]byte{0xaa})
	req.SetCode(codes.Content)
	req.SetObserve(1)
	cc.ProcessReceivedMessageWithHandler(req, cc.handleReq)
	require.True(t, called)
	require.Equal(t, 1, session.writtenCount())
	wire, err := session.lastWritten().MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.Equal(t, []byte{0x60, 0x00, 0x12, 0x34}, wire)
}

func TestUnmatchedEmptyResetIsNotDispatched(t *testing.T) {
	session := newRecordingSession()
	cfg := DefaultConfig
	called := false
	cfg.Handler = func(*responsewriter.ResponseWriter[*Conn], *pool.Message) { called = true }
	cc := NewConnWithOpts(session, &cfg)
	defer func() { require.NoError(t, cc.Close()) }()

	reset := cc.AcquireMessage(context.Background())
	reset.SetType(message.Reset)
	reset.SetCode(codes.Empty)
	reset.SetMessageID(0x1234)
	require.True(t, cc.handleSpecialMessages(reset))
	require.False(t, called)
	require.Zero(t, session.writtenCount())
}
