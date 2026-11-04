package qblock

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestRFC9177ResponseFamilyAndConsistentBodyType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		first      message.Type
		second     message.Type
		mixedBlock bool
		valid      bool
	}{
		{"non", message.NonConfirmable, message.NonConfirmable, false, true},
		{"ack-then-con", message.Acknowledgement, message.Confirmable, false, true},
		{"con-then-ack", message.Confirmable, message.Acknowledgement, false, true},
		{"non-then-con", message.NonConfirmable, message.Confirmable, false, false},
		{"con-then-non", message.Confirmable, message.NonConfirmable, false, false},
		{"mixed-family", message.NonConfirmable, message.NonConfirmable, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
			cc := newTestClient()
			engine := New(cc, nil)
			req := pool.NewMessage(context.Background())
			req.SetCode(codes.GET)
			req.SetToken([]byte("body"))
			receiver := q2BodyReceiver[*testClient]{
				b: engine, req: req, szx: SZX16, maxSZX: SZX16, requestedSet: -1,
				exchange: new(qObserveExchange),
			}
			defer func() {
				receiver.b.qReleaseBytes(receiver.retainedBytes)
				require.Equal(t, bytesBefore, qRetainedBytes.Load())
				require.Equal(t, transfersBefore, qRetainedTransfers.Load())
			}()
			first := qObservePayload(t, SZX16, 0, 32, 1, nil)
			first.SetType(tc.first)
			complete, err := receiver.accept(first)
			require.NoError(t, err)
			require.False(t, complete)
			second := qObservePayload(t, SZX16, 1, 32, 1, nil)
			second.SetType(tc.second)
			if tc.mixedBlock {
				second.SetOptionUint32(message.Block2, 0)
			}
			complete, err = receiver.accept(second)
			if tc.valid {
				require.NoError(t, err)
				require.True(t, complete)
			} else {
				require.ErrorIs(t, err, ErrInvalidOptionBlock2)
				require.False(t, complete)
				require.False(t, receiver.assembly.Complete())
			}
		})
	}
}
