package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func TestManagerQ1SenderTraceRepairsLossAndReleasesState(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("tag"))
	require.NoError(t, err)
	now := time.Unix(100, 0)
	meta := Metadata{Size: 11 * 16, SZX: blockwise.SZX16, Identity: []byte("body")}

	outputs, err := m.StartSender(key, message.Token{1}, Q1, meta, bytes.Repeat([]byte{'q'}, int(meta.Size)), now, 0)
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, outputNumbers(outputs))
	for i := range outputs {
		require.NoError(t, m.BindToken(1, message.Token{byte(i + 2)}))
	}

	// Data block 5 and the first missing-block report are lost on the wire.
	outputs = m.Tick(now.Add(2 * time.Second))
	require.Equal(t, []uint32{10}, outputNumbers(outputs))
	require.NoError(t, m.BindToken(1, message.Token{12}))

	outputs, err = m.Control(Control{Token: message.Token{12}, Missing: []uint32{5}}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, []uint32{5}, outputNumbers(outputs))
	outputs, err = m.Control(Control{Token: message.Token{12}}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{outputs[0].Action.Kind, outputs[1].Action.Kind})
	require.Zero(t, m.Active())
	require.Empty(t, m.byOperation)
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)
	_, ok := m.NextDeadline()
	require.False(t, ok)

	outputs, err = m.Control(Control{Token: message.Token{1}}, now)
	require.ErrorIs(t, err, ErrUnknownTransfer)
	require.Empty(t, outputs)
}

func TestManagerInboundQ2TraceDeliversThenReleases(t *testing.T) {
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	key, err := NewOperationKey([]byte("request"), []byte("etag"))
	require.NoError(t, err)
	fragment := Fragment{
		Operation: key,
		Token:     message.Token{1},
		Kind:      Q2,
		Metadata:  Metadata{Size: 16, SZX: blockwise.SZX16, Identity: []byte("representation")},
		Block:     Block{SZX: blockwise.SZX16},
		Payload:   bytes.Repeat([]byte{'q'}, 16),
	}

	outputs, err := m.StartReceiver(fragment, time.Unix(100, 0))
	require.NoError(t, err)
	require.Equal(t, []ActionKind{Deliver, Release}, []ActionKind{outputs[0].Action.Kind, outputs[1].Action.Kind})
	require.Zero(t, m.Active())
	require.Empty(t, m.byOperation)
	require.Empty(t, m.byToken)
	require.Zero(t, m.retained)
}
