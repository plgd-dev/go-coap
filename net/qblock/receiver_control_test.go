package qblock

import (
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func deferredReceiverFixture(t *testing.T, size uint32) (*Receiver, TransferConfig, Metadata, time.Time) {
	t.Helper()
	cfg := DefaultTransferConfig()
	cfg.MaxPayloads = 2
	cfg.Lifetime = 60 * time.Second
	meta := Metadata{Size: size, SZX: blockwise.SZX16, Identity: []byte("body")}
	now := time.Unix(100, 0)
	r, err := NewDeferredReceiver(Q1, cfg, meta, now)
	require.NoError(t, err)
	return r, cfg, meta, now
}

func receiveDeferredBlock(t *testing.T, r *Receiver, meta Metadata, number uint32, now time.Time) []Action {
	t.Helper()
	more := number+1 < (meta.Size+15)/16
	actions, err := r.Receive(meta, Block{Number: number, More: more, SZX: blockwise.SZX16}, make([]byte, 16), now)
	require.NoError(t, err)
	return actions
}

func TestDeferredReceiverRetriesBeginAtCommit(t *testing.T) {
	r, cfg, meta, now := deferredReceiverFixture(t, 48)
	require.Empty(t, receiveDeferredBlock(t, r, meta, 0, now))
	require.Empty(t, r.Tick(now.Add(4*time.Second)))
	intents := r.PendingControls()
	require.Len(t, intents, 1)
	require.Equal(t, RequestMissing, intents[0].Action.Kind)
	require.Equal(t, []uint32{1}, intents[0].Action.Numbers)
	require.Zero(t, r.retries)
	require.Empty(t, r.lastMissing)
	deadline, ok := r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(cfg.Lifetime), deadline)
	require.Empty(t, r.Tick(now.Add(20*time.Second)))
	require.Empty(t, r.CommitControl(intents[0].Revision, now.Add(20*time.Second)))
	require.Equal(t, uint32(1), r.retries)
	deadline, ok = r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(28*time.Second), deadline)
	require.Empty(t, r.CommitControl(intents[0].Revision, now.Add(21*time.Second)))
	require.Equal(t, uint32(1), r.retries)
}

func TestDeferredReceiverProgressInvalidatesIntent(t *testing.T) {
	r, _, meta, now := deferredReceiverFixture(t, 48)
	receiveDeferredBlock(t, r, meta, 0, now)
	r.Tick(now.Add(4 * time.Second))
	old := r.PendingControls()[0].Revision
	receiveDeferredBlock(t, r, meta, 1, now.Add(5*time.Second))
	require.Equal(t, uint64(2), r.progress)
	newPending := r.PendingControls()
	require.Len(t, newPending, 1)
	require.NotEqual(t, old, newPending[0].Revision)
	require.Equal(t, SendContinue, newPending[0].Action.Kind)
	require.Empty(t, r.CommitControl(old, now.Add(6*time.Second)))
	require.Zero(t, r.retries)

	_, err := r.Receive(meta, Block{Number: 2, More: true, SZX: blockwise.SZX16}, make([]byte, 16), now.Add(6*time.Second))
	require.Error(t, err)
	require.Equal(t, uint64(2), r.progress)
	require.Equal(t, newPending[0].Revision, r.PendingControls()[0].Revision)
	require.Equal(t, Duplicate, receiveDeferredBlock(t, r, meta, 1, now.Add(6*time.Second))[0].Kind)
	require.Equal(t, uint64(2), r.progress)
	require.Equal(t, newPending[0].Revision, r.PendingControls()[0].Revision)
}

func TestDeferredReceiverContinueCommitsIndependently(t *testing.T) {
	r, _, meta, now := deferredReceiverFixture(t, 80)
	receiveDeferredBlock(t, r, meta, 0, now)
	receiveDeferredBlock(t, r, meta, 1, now.Add(time.Second))
	continueIntent := r.PendingControls()
	require.Len(t, continueIntent, 1)
	require.Equal(t, SendContinue, continueIntent[0].Action.Kind)
	require.Empty(t, r.CommitControl(continueIntent[0].Revision, now.Add(time.Second)))
	require.Equal(t, uint32(2), r.continued)
	require.Empty(t, r.PendingControls())
	receiveDeferredBlock(t, r, meta, 4, now.Add(2*time.Second))
	missing := r.PendingControls()
	require.Len(t, missing, 1)
	require.Equal(t, RequestMissing, missing[0].Action.Kind)
	require.Equal(t, []uint32{2, 3}, missing[0].Action.Numbers)
	require.Zero(t, r.retries)
	require.Equal(t, Duplicate, receiveDeferredBlock(t, r, meta, 4, now.Add(3*time.Second))[0].Kind)
	require.Equal(t, missing[0].Revision, r.PendingControls()[0].Revision)
	require.Equal(t, uint32(2), r.continued)
}

func TestDeferredReceiverOutOfOrderCommitsKeepRetryDelay(t *testing.T) {
	r, _, meta, now := deferredReceiverFixture(t, 80)
	receiveDeferredBlock(t, r, meta, 0, now)
	receiveDeferredBlock(t, r, meta, 1, now.Add(time.Second))
	receiveDeferredBlock(t, r, meta, 4, now.Add(2*time.Second))
	intents := r.PendingControls()
	require.Len(t, intents, 2)
	require.Equal(t, SendContinue, intents[0].Action.Kind)
	require.Equal(t, RequestMissing, intents[1].Action.Kind)
	r.CommitControl(intents[1].Revision, now.Add(3*time.Second))
	require.Equal(t, uint32(1), r.retries)
	r.CommitControl(intents[0].Revision, now.Add(4*time.Second))
	deadline, ok := r.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(12*time.Second), deadline)
}

func TestDeferredReceiverZeroRetriesAndLateCommit(t *testing.T) {
	cfg := DefaultTransferConfig()
	cfg.MaxPayloads = 2
	cfg.NonMaxRetransmit = 0
	cfg.Lifetime = 60 * time.Second
	meta := Metadata{Size: 48, SZX: blockwise.SZX16}
	now := time.Unix(100, 0)
	r, err := NewDeferredReceiver(Q1, cfg, meta, now)
	require.NoError(t, err)
	receiveDeferredBlock(t, r, meta, 0, now)
	actions := r.Tick(now.Add(4 * time.Second))
	require.Equal(t, []ActionKind{Complete, Release}, []ActionKind{actions[0].Kind, actions[1].Kind})
	require.Empty(t, r.PendingControls())
	require.Empty(t, r.CommitControl(1, now.Add(cfg.Lifetime)))
	require.False(t, func() bool { _, ok := r.NextDeadline(); return ok }())
}

func TestDeferredReceiverCompletesWithPendingControl(t *testing.T) {
	r, _, meta, now := deferredReceiverFixture(t, 48)
	receiveDeferredBlock(t, r, meta, 0, now)
	r.Tick(now.Add(4 * time.Second))
	old := r.PendingControls()[0].Revision
	receiveDeferredBlock(t, r, meta, 2, now.Add(5*time.Second))
	actions := receiveDeferredBlock(t, r, meta, 1, now.Add(6*time.Second))
	require.Len(t, actions, 1)
	require.Equal(t, Deliver, actions[0].Kind)
	require.Empty(t, r.PendingControls())
	require.Empty(t, r.CommitControl(old, now.Add(7*time.Second)))
	require.Equal(t, Duplicate, receiveDeferredBlock(t, r, meta, 1, now.Add(8*time.Second))[0].Kind)
}
