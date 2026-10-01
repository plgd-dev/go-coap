package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type capabilityResult struct {
	supported bool
	err       error
}

type capabilitySession struct{ *qblockTestSession }

func (s *capabilitySession) Done() <-chan struct{} { return s.ctx.Done() }

func capabilityConn(t *testing.T) (*Conn, *qblockTestSession, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &qblockTestSession{ctx: ctx, writeCh: make(chan struct{}, 16)}
	cfg := DefaultConfig
	cc := NewConnWithOpts(&capabilitySession{s}, &cfg)
	t.Cleanup(func() { cancel(); s.closeForTest() })
	return cc, s, cancel
}

func startCapabilityProbe(t *testing.T, cc *Conn, s *qblockTestSession, ctx context.Context) (<-chan capabilityResult, qblockTestWrite) {
	t.Helper()
	result := make(chan capabilityResult, 1)
	go func() { ok, err := cc.ProbeQBlock(ctx, ""); result <- capabilityResult{ok, err} }()
	select {
	case <-s.writeCh:
	case <-time.After(time.Second):
		t.Fatal("probe not written")
	}
	return result, s.writesSnapshot()[len(s.writesSnapshot())-1]
}

func capabilityReply(cc *Conn, sent qblockTestWrite) *pool.Message {
	r := cc.AcquireMessage(cc.Context())
	r.SetCode(codes.Content)
	r.SetType(message.Acknowledgement)
	r.SetMessageID(sent.mid)
	r.SetToken(sent.token)
	r.SetOptionUint32(message.QBlock2, 8)
	r.SetOptionBytes(message.ETag, []byte{1})
	r.SetOptionUint32(message.Size2, 1000000)
	r.SetBody(bytes.NewReader([]byte("0123456789abcdef")))
	return r
}

func ingestCapability(t *testing.T, cc *Conn, r *pool.Message) {
	t.Helper()
	wire, err := r.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	cc.ReleaseMessage(r)
	require.NoError(t, cc.Process(nil, wire))
}

func awaitCapability(t *testing.T, result <-chan capabilityResult) capabilityResult {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(time.Second):
		t.Fatal("probe did not finish")
		return capabilityResult{}
	}
}

func TestQBlockCapabilityProbeWireAndResponse(t *testing.T) {
	for _, more := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "first_block_only"}[more], func(t *testing.T) {
			cc, s, _ := capabilityConn(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			require.Equal(t, codes.GET, sent.code)
			require.Equal(t, message.Confirmable, sent.typ)
			path, err := sent.options.Path()
			require.NoError(t, err)
			require.Equal(t, "/.well-known/core", path)
			value, err := sent.options.GetUint32(message.QBlock2)
			require.NoError(t, err)
			require.Zero(t, value)
			require.Empty(t, sent.payload)
			require.Len(t, sent.options, 3)
			r := capabilityReply(cc, sent)
			if !more {
				r.SetOptionUint32(message.QBlock2, 0)
				r.SetOptionUint32(message.Size2, 2)
				r.SetBody(bytes.NewReader([]byte("ok")))
			}
			ingestCapability(t, cc, r)
			got := awaitCapability(t, result)
			require.NoError(t, got.err)
			require.True(t, got.supported)
			require.Len(t, s.writesSnapshot(), 1, "single-block probe must not request the rest")
			require.Nil(t, cc.qblockClient, "probe must not instantiate the NON engine")
			require.Zero(t, cc.tokenReservations.Length())
			require.Zero(t, cc.midHandlerContainer.Length())
		})
	}
}

func TestQBlockCapabilityProbeResponseValidation(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*pool.Message)
		wantErr bool
	}{
		{"bad_option", func(r *pool.Message) { r.SetCode(codes.BadOption); r.ResetOptionsTo(nil); r.SetBody(nil) }, false},
		{"q_less_success", func(r *pool.Message) { r.Remove(message.QBlock2); r.Remove(message.Size2); r.Remove(message.ETag) }, false},
		{"duplicate_q", func(r *pool.Message) { r.AddOptionUint32(message.QBlock2, 8) }, true},
		{"long_q", func(r *pool.Message) { r.SetOptionBytes(message.QBlock2, []byte{0, 0, 0, 8}) }, true},
		{"mixed", func(r *pool.Message) { r.SetOptionUint32(message.Block2, 0) }, true},
		{"q1", func(r *pool.Message) { r.SetOptionUint32(message.QBlock1, 0) }, true},
		{"wrong_block", func(r *pool.Message) { r.SetOptionUint32(message.QBlock2, 24) }, true},
		{"larger_szx", func(r *pool.Message) { r.SetOptionUint32(message.QBlock2, 9) }, true},
		{"missing_etag", func(r *pool.Message) { r.Remove(message.ETag) }, true},
		{"missing_size", func(r *pool.Message) { r.Remove(message.Size2) }, true},
		{"invalid_more", func(r *pool.Message) { r.SetOptionUint32(message.Size2, 16) }, true},
		{"short_more", func(r *pool.Message) { r.SetBody(bytes.NewReader([]byte("short"))) }, true},
		{"invalid_final_size", func(r *pool.Message) { r.SetOptionUint32(message.QBlock2, 0) }, true},
		{"unexpected_code", func(r *pool.Message) { r.SetCode(codes.ServiceUnavailable) }, true},
		{"reset", func(r *pool.Message) {
			r.SetType(message.Reset)
			r.SetCode(codes.Empty)
			r.SetToken(nil)
			r.ResetOptionsTo(nil)
			r.SetBody(nil)
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, s, _ := capabilityConn(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			r := capabilityReply(cc, sent)
			tt.change(r)
			ingestCapability(t, cc, r)
			got := awaitCapability(t, result)
			require.False(t, got.supported)
			if tt.wantErr {
				require.Error(t, got.err)
			} else {
				require.NoError(t, got.err)
			}
			require.Zero(t, cc.tokenReservations.Length())
			require.Zero(t, cc.midHandlerContainer.Length())
		})
	}
}

func TestQBlockCapabilityProbeCorrelationAndSeparateResponse(t *testing.T) {
	for _, separate := range []message.Type{message.Confirmable, message.NonConfirmable} {
		t.Run(separate.String(), func(t *testing.T) {
			cc, s, _ := capabilityConn(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			wrong := capabilityReply(cc, sent)
			wrong.SetToken([]byte{99})
			ingestCapability(t, cc, wrong)
			wrong = capabilityReply(cc, sent)
			wrong.SetMessageID(sent.mid + 1)
			ingestCapability(t, cc, wrong)
			require.Equal(t, 1, cc.midHandlerContainer.Length(), "unmatched ACK must not stop retransmission")
			select {
			case <-result:
				t.Fatal("unmatched response completed probe")
			default:
			}
			ack := cc.AcquireMessage(cc.Context())
			ack.SetType(message.Acknowledgement)
			ack.SetMessageID(sent.mid)
			ack.SetCode(codes.Empty)
			ingestCapability(t, cc, ack)
			require.Zero(t, cc.midHandlerContainer.Length())
			select {
			case <-result:
				t.Fatal("empty ACK is not capability evidence")
			default:
			}
			reply := capabilityReply(cc, sent)
			reply.SetType(separate)
			reply.SetMessageID(sent.mid + 100)
			ingestCapability(t, cc, reply)
			got := awaitCapability(t, result)
			require.NoError(t, got.err)
			require.True(t, got.supported)
			writes := s.writesSnapshot()
			if separate == message.Confirmable {
				require.Len(t, writes, 2)
				require.Equal(t, message.Acknowledgement, writes[1].typ)
				require.Equal(t, sent.mid+100, writes[1].mid)
				require.Empty(t, writes[1].token)
			} else {
				require.Len(t, writes, 1)
			}
		})
	}
}

func TestQBlockCapabilityProbeTimeout(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, _ := startCapabilityProbe(t, cc, s, ctx)
	got := awaitCapability(t, result)
	require.False(t, got.supported)
	require.ErrorIs(t, got.err, context.DeadlineExceeded)
	require.Zero(t, cc.tokenReservations.Length())
	require.Zero(t, cc.midHandlerContainer.Length())
}
