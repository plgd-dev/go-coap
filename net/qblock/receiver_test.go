package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func receiverMeta(blocks uint32) Metadata {
	return Metadata{Size: blocks * 16, SZX: blockwise.SZX16, Identity: []byte("operation")}
}

func receiveTestBlock(t *testing.T, r *Receiver, meta Metadata, number uint32, now time.Time) []Action {
	t.Helper()
	count := (meta.Size + 15) / 16
	payload := bytes.Repeat([]byte{byte(number)}, 16)
	actions, err := r.Receive(meta, Block{Number: number, More: number+1 < count, SZX: meta.SZX}, payload, now)
	require.NoError(t, err)
	return actions
}

func actionsOfKind(actions []Action, kind ActionKind) []Action {
	result := make([]Action, 0, len(actions))
	for _, action := range actions {
		if action.Kind == kind {
			result = append(result, action)
		}
	}
	return result
}

func TestReceiverFinalFirstReorderAndSingleQ1Delivery(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(21)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)

	actions := receiveTestBlock(t, r, meta, 20, now)
	require.Equal(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, actionsOfKind(actions, RequestMissing)[0].Numbers)

	for number := uint32(9); number > 0; number-- {
		require.Empty(t, receiveTestBlock(t, r, meta, number, now.Add(time.Second)))
	}
	actions = receiveTestBlock(t, r, meta, 0, now.Add(time.Second))
	require.Equal(t, uint32(9), actionsOfKind(actions, SendContinue)[0].Through)

	for number := uint32(19); number > 10; number-- {
		require.Empty(t, receiveTestBlock(t, r, meta, number, now.Add(2*time.Second)))
	}
	actions = receiveTestBlock(t, r, meta, 10, now.Add(2*time.Second))
	deliveries := actionsOfKind(actions, Deliver)
	require.Len(t, deliveries, 1)
	require.Len(t, deliveries[0].Payload, int(meta.Size))
	require.Equal(t, byte(0), deliveries[0].Payload[0])
	require.Equal(t, byte(20), deliveries[0].Payload[20*16])
	deliveries[0].Payload[0] = 99

	deadline, ok := r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(247*time.Second), deadline)
	actions = receiveTestBlock(t, r, meta, 0, now.Add(100*time.Second))
	require.Equal(t, []Action{{Kind: Duplicate, Block: Block{Number: 0, More: true, SZX: blockwise.SZX16}}}, actions)
	gotDeadline, ok := r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, deadline, gotDeadline)
	require.Equal(t, []Action{{Kind: Release}}, r.Tick(deadline))
	require.Empty(t, r.Tick(deadline.Add(time.Second)))
}

func TestReceiverQ2DeliversAndReleasesImmediately(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(1)
	r, err := NewReceiver(Q2, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	actions := receiveTestBlock(t, r, meta, 0, now)
	require.Equal(t, []ActionKind{Deliver, Release}, []ActionKind{actions[0].Kind, actions[1].Kind})
	_, ok := r.NextDeadline()
	require.False(t, ok)
	_, err = r.Receive(meta, Block{Number: 0, SZX: meta.SZX}, bytes.Repeat([]byte{0}, 16), now)
	require.ErrorIs(t, err, ErrClosed)
}

func TestReceiverGapReportsAreBoundedAndNotRepeatedWithoutChange(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(21)
	cfg := DefaultTransferConfig()
	cfg.MaxPayloads = 4
	r, err := NewReceiver(Q1, cfg, meta, now)
	require.NoError(t, err)

	actions := receiveTestBlock(t, r, meta, 4, now)
	require.Equal(t, []uint32{0, 1, 2, 3}, actionsOfKind(actions, RequestMissing)[0].Numbers)
	require.Empty(t, receiveTestBlock(t, r, meta, 5, now.Add(time.Second)))
	require.Empty(t, receiveTestBlock(t, r, meta, 0, now.Add(2*time.Second)))
	actions = receiveTestBlock(t, r, meta, 6, now.Add(3*time.Second))
	require.Equal(t, []uint32{1, 2, 3}, actionsOfKind(actions, RequestMissing)[0].Numbers)
}

func TestReceiverImmediateGapReportConsumesRetryBudget(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(11)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	actions := receiveTestBlock(t, r, meta, 10, now)
	require.Equal(t, RequestMissing, actions[0].Kind)
	deadline, _ := r.NextDeadline()
	require.Equal(t, now.Add(8*time.Second), deadline)
	for _, tt := range []struct {
		at   time.Duration
		next time.Duration
	}{
		{at: 8 * time.Second, next: 24 * time.Second},
		{at: 24 * time.Second, next: 56 * time.Second},
		{at: 56 * time.Second, next: 120 * time.Second},
	} {
		require.Equal(t, RequestMissing, r.Tick(now.Add(tt.at))[0].Kind)
		deadline, _ = r.NextDeadline()
		require.Equal(t, now.Add(tt.next), deadline)
	}
	actions = r.Tick(now.Add(120 * time.Second))
	require.Equal(t, Complete, actions[0].Kind)
	require.ErrorIs(t, actions[0].Err, ErrRetriesExhausted)
}

func TestReceiverZeroRetriesSuppressesImmediateGapReport(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(11)
	cfg := DefaultTransferConfig()
	cfg.NonMaxRetransmit = 0
	r, err := NewReceiver(Q1, cfg, meta, now)
	require.NoError(t, err)
	require.Empty(t, receiveTestBlock(t, r, meta, 10, now))
	actions := r.Tick(now.Add(4 * time.Second))
	require.Equal(t, Complete, actions[0].Kind)
	require.ErrorIs(t, actions[0].Err, ErrRetriesExhausted)
}

func TestReceiverCollapsesNewContiguousBoundariesIntoLatestContinue(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(31)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	for number := uint32(1); number < 30; number++ {
		receiveTestBlock(t, r, meta, number, now)
	}
	actions := receiveTestBlock(t, r, meta, 0, now)
	continues := actionsOfKind(actions, SendContinue)
	require.Equal(t, []Action{{Kind: SendContinue, Through: 29}}, continues)
}

func TestReceiverRetriesAtExactDeadlinesAndDuplicateDoesNotRefresh(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(2)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	deadline, ok := r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(247*time.Second), deadline)
	require.Empty(t, r.Tick(now.Add(4*time.Second)))

	require.Empty(t, receiveTestBlock(t, r, meta, 0, now))
	deadline, _ = r.NextDeadline()
	require.Equal(t, now.Add(4*time.Second), deadline)
	actions := receiveTestBlock(t, r, meta, 0, now.Add(3*time.Second))
	require.Equal(t, Duplicate, actions[0].Kind)
	gotDeadline, _ := r.NextDeadline()
	require.Equal(t, deadline, gotDeadline)

	for _, tt := range []struct {
		at   time.Duration
		next time.Duration
	}{
		{at: 4 * time.Second, next: 12 * time.Second},
		{at: 12 * time.Second, next: 28 * time.Second},
		{at: 28 * time.Second, next: 60 * time.Second},
		{at: 60 * time.Second, next: 124 * time.Second},
	} {
		actions = r.Tick(now.Add(tt.at))
		require.Equal(t, []uint32{1}, actionsOfKind(actions, RequestMissing)[0].Numbers)
		deadline, _ = r.NextDeadline()
		require.Equal(t, now.Add(tt.next), deadline)
	}
	actions = r.Tick(now.Add(124 * time.Second))
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{actions[0].Kind, actions[1].Kind})
	require.ErrorIs(t, actions[0].Err, ErrRetriesExhausted)
	_, ok = r.NextDeadline()
	require.False(t, ok)
}

func TestReceiverProgressResetsRetryBudgetAndDeadline(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(3)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	require.Empty(t, receiveTestBlock(t, r, meta, 0, now))
	require.Equal(t, RequestMissing, r.Tick(now.Add(4 * time.Second))[0].Kind)
	require.Empty(t, receiveTestBlock(t, r, meta, 1, now.Add(5*time.Second)))
	deadline, _ := r.NextDeadline()
	require.Equal(t, now.Add(9*time.Second), deadline)
	require.Equal(t, RequestMissing, r.Tick(deadline)[0].Kind)
	next, _ := r.NextDeadline()
	require.Equal(t, now.Add(17*time.Second), next)
}

func TestReceiverInvalidFragmentIsAtomic(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(2)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	require.Empty(t, receiveTestBlock(t, r, meta, 0, now))
	deadline, _ := r.NextDeadline()
	beforeCount := len(r.body.blocks)

	changed := meta
	changed.Identity = []byte("other")
	_, err = r.Receive(changed, Block{Number: 1, SZX: meta.SZX}, bytes.Repeat([]byte{1}, 16), now.Add(time.Second))
	require.Error(t, err)
	_, err = r.Receive(meta, Block{Number: 1, SZX: meta.SZX}, []byte{1}, now.Add(time.Second))
	require.Error(t, err)
	require.Len(t, r.body.blocks, beforeCount)
	gotDeadline, _ := r.NextDeadline()
	require.Equal(t, deadline, gotDeadline)
}

func TestReceiverCancelAndLifetimeClearState(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(2)
	r, err := NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	require.Empty(t, receiveTestBlock(t, r, meta, 0, now))
	actions := r.Cancel(nil)
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{actions[0].Kind, actions[1].Kind})
	require.ErrorIs(t, actions[0].Err, ErrCanceled)
	require.Nil(t, r.body)
	require.Empty(t, r.Cancel(ErrCanceled))

	r, err = NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.NoError(t, err)
	actions = r.Tick(now.Add(247 * time.Second))
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{actions[0].Kind, actions[1].Kind})
	require.ErrorIs(t, actions[0].Err, ErrExpired)
}

func TestReceiverConstructorValidation(t *testing.T) {
	now := time.Unix(100, 0)
	meta := receiverMeta(1)
	for _, kind := range []Kind{0, 3} {
		_, err := NewReceiver(kind, DefaultTransferConfig(), meta, now)
		require.Error(t, err)
	}
	cfg := DefaultTransferConfig()
	cfg.MaxPayloads = 0
	_, err := NewReceiver(Q1, cfg, meta, now)
	require.Error(t, err)
	meta.Size = cfg.MaxBodySize + 1
	_, err = NewReceiver(Q1, DefaultTransferConfig(), meta, now)
	require.Error(t, err)
}
