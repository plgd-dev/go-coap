package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

func TestQBlockCapabilityProbeRawEmptyControl(t *testing.T) {
	for _, typ := range []message.Type{message.Acknowledgement, message.Reset} {
		for _, suffix := range []byte{0x40, 0xff} {
			t.Run(typ.String()+map[byte]string{0x40: "/empty_etag", 0xff: "/empty_payload_marker"}[suffix], func(t *testing.T) {
				cc, s, clock, _ := capabilityEndpointConn(t)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				result, sent := startCapabilityProbe(t, cc, s, ctx)
				wire := []byte{0x40 | byte(typ)<<4, 0, byte(sent.mid >> 8), byte(sent.mid), suffix}
				require.NoError(t, cc.Process(nil, wire))
				require.Equal(t, 1, cc.midHandlerContainer.Length(), "malformed control cannot acknowledge")
				require.False(t, cc.qblockClient.endpoint.admit(99, qblockProbeControl, 0, clock.Now()))
				cc.CheckExpirations(time.Now().Add(3 * time.Second))
				require.Len(t, s.writesSnapshot(), 2)
				ingestCapability(t, cc, capabilityReply(cc, sent))
				require.True(t, awaitCapability(t, result).supported)
			})
		}
	}
}

func TestQBlockCapabilityProbeRawMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    message.OptionID
		value []byte
	}{
		{"empty_duplicate_etag", message.ETag, nil},
		{"long_duplicate_size", message.Size2, []byte{0, 0, 0, 0, 1}},
		{"malformed_observe", message.Observe, []byte{0, 0, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc, s, _ := capabilityConn(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, sent := startCapabilityProbe(t, cc, s, ctx)
			r := capabilityReply(cc, sent)
			r.AddOptionBytes(tc.id, tc.value)
			ingestCapability(t, cc, r)
			got := awaitCapability(t, result)
			require.False(t, got.supported)
			require.Error(t, got.err)
		})
	}
}

func TestQBlockCapabilityProbeSeparateCONRetransmission(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	var deliveries atomic.Int32
	cc.tokenHandlerContainer.Store(sent.token.Hash(), func(*responsewriter.ResponseWriter[*Conn], *pool.Message) { deliveries.Add(1) })
	defer cc.tokenHandlerContainer.Delete(sent.token.Hash())
	r := capabilityReply(cc, sent)
	r.SetType(message.Confirmable)
	r.SetMessageID((sent.mid + 100) & 0xffff)
	wire, err := r.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	cc.ReleaseMessage(r)
	require.NoError(t, cc.Process(nil, wire))
	require.True(t, awaitCapability(t, result).supported)
	require.NoError(t, cc.Process(nil, wire))
	require.Eventually(t, func() bool { return len(s.writesSnapshot()) == 3 }, time.Second, time.Millisecond)
	for _, ack := range s.writesSnapshot()[1:] {
		require.Equal(t, message.Acknowledgement, ack.typ)
		require.Equal(t, codes.Empty, ack.code)
		require.Equal(t, (sent.mid+100)&0xffff, ack.mid)
		require.Empty(t, ack.token)
	}
	require.Nil(t, cc.qblockClient)
	require.Zero(t, deliveries.Load(), "probe and duplicate must not reach application delivery")
}

type failingProbeACKCache struct{ MessageCache }

func (failingProbeACKCache) Store(string, *pool.Message) error {
	return errors.New("probe ACK cache failed")
}

func TestQBlockCapabilityProbeACKCacheFailure(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	cc.responseMsgCache = failingProbeACKCache{cc.responseMsgCache}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	r := capabilityReply(cc, sent)
	r.SetType(message.Confirmable)
	r.SetMessageID((sent.mid + 100) & 0xffff)
	ingestCapability(t, cc, r)
	got := awaitCapability(t, result)
	require.False(t, got.supported)
	require.EqualError(t, got.err, "probe ACK cache failed")
	require.Len(t, s.writesSnapshot(), 1)
	require.Zero(t, cc.midHandlerContainer.Length())
	require.Zero(t, cc.tokenReservations.Length())
}

func TestQBlockCapabilityProbeEmptyResponsePayloadMarker(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	r := capabilityReply(cc, sent)
	r.SetOptionUint32(message.QBlock2, 0)
	r.SetOptionUint32(message.Size2, 0)
	r.SetBody(nil)
	wire, err := r.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	wire = append(append([]byte(nil), wire...), 0xff)
	cc.ReleaseMessage(r)
	require.NoError(t, cc.Process(nil, wire))
	got := awaitCapability(t, result)
	require.False(t, got.supported)
	require.Error(t, got.err)
}

func TestQBlockCapabilityProbeEmptyACKReleasesNSTART(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	ack := []byte{0x60, 0, byte(sent.mid >> 8), byte(sent.mid)}
	require.NoError(t, cc.Process(nil, ack))
	ordinaryCtx, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	req := cc.AcquireMessage(ordinaryCtx)
	defer cc.ReleaseMessage(req)
	req.SetType(message.Confirmable)
	req.SetCode(codes.GET)
	req.SetToken([]byte{0xfa})
	req.SetMessageID((sent.mid + 200) & 0xffff)
	done := make(chan error, 1)
	go func() { done <- cc.WriteMessage(req) }()
	require.Eventually(t, func() bool { return len(s.writesSnapshot()) == 2 }, time.Second, time.Millisecond)
	require.NoError(t, cc.Process(nil, []byte{0x60, 0, byte(req.MessageID() >> 8), byte(req.MessageID())}))
	require.NoError(t, <-done)
	select {
	case <-result:
		t.Fatal("probe must still await its separate response")
	default:
	}
	ingestCapability(t, cc, capabilityReply(cc, sent))
	require.True(t, awaitCapability(t, result).supported)
	// Completion must not release the same semaphore weight twice.
	require.NoError(t, cc.acquireOutstandingInteraction(context.Background()))
	cc.releaseOutstandingInteraction()
}

func TestQBlockCapabilityProbeStaleIngressPreservesSuccessorMID(t *testing.T) {
	cc, s, _ := capabilityConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	result, sent := startCapabilityProbe(t, cc, s, ctx)
	cc.qblockProbeMu.Lock()
	p := cc.qblockProbe
	cc.qblockProbeMu.Unlock()
	// Pause ingress after it captures the active probe, before touching MID state.
	resume := make(chan struct{})
	done := make(chan bool, 1)
	r := capabilityReply(cc, sent)
	wire, err := r.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	go func() {
		<-resume
		done <- cc.handleQBlockProbeResponse(p, r, wire)
	}()
	cancel()
	require.ErrorIs(t, awaitCapability(t, result).err, context.Canceled)
	successor := &midElement{}
	successor.private.msg = cc.AcquireMessage(cc.Context())
	cc.midHandlerContainer.Store(sent.mid, successor)
	defer successor.ReleaseMessage(cc)
	close(resume)
	require.True(t, <-done)
	got, exists := cc.midHandlerContainer.Load(sent.mid)
	require.True(t, exists, "stale probe ingress must not delete the successor")
	require.Same(t, successor, got)
	require.NotNil(t, successor.private.msg)
	cc.midHandlerContainer.Delete(sent.mid)
}
