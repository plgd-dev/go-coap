package qblock

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/stretchr/testify/require"
)

func newTestSender(t *testing.T) (*Sender, time.Time) {
	t.Helper()
	now := time.Unix(100, 0)
	meta := Metadata{Size: 21 * 16, SZX: blockwise.SZX16, Identity: []byte("operation")}
	s, err := NewSender(Q1, DefaultTransferConfig(), meta, bytes.Repeat([]byte{7}, int(meta.Size)), now, 0)
	require.NoError(t, err)
	return s, now
}

func sentNumbers(actions []Action) []uint32 {
	var result []uint32
	for _, a := range actions {
		if a.Kind == SendBlock {
			result = append(result, a.Block.Number)
		}
	}
	return result
}

func TestSenderFixedSetsAndContinue(t *testing.T) {
	s, now := newTestSender(t)
	actions, err := s.Start(now)
	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, sentNumbers(actions))
	_, err = s.Start(now)
	require.Error(t, err)
	require.Empty(t, s.Tick(now.Add(time.Second)))
	for _, n := range []uint32{8, 19, 20} {
		a, e := s.Continue(n, now)
		require.NoError(t, e)
		require.Empty(t, a)
	}
	actions, err = s.Continue(9, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, sentNumbers(actions))
	actions, err = s.Continue(9, now)
	require.NoError(t, err)
	require.Empty(t, actions)
	actions = s.Tick(now.Add(2 * time.Second))
	require.Equal(t, []uint32{20}, sentNumbers(actions))
	require.False(t, actions[0].Block.More)
	require.Empty(t, s.Tick(now.Add(4*time.Second)))
	actions = s.Finish(nil)
	require.Len(t, actions, 2)
	require.Equal(t, Complete, actions[0].Kind)
	require.Equal(t, Release, actions[1].Kind)
	require.Empty(t, s.Finish(nil))
	require.Empty(t, s.Cancel(ErrCanceled))
	_, ok := s.NextDeadline()
	require.False(t, ok)
}

func TestSenderRepairBoundaries(t *testing.T) {
	s, now := newTestSender(t)
	_, err := s.Start(now)
	require.NoError(t, err)
	s.Tick(now.Add(2 * time.Second))
	actions, err := s.Repair([]uint32{8, 12}, now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, []uint32{8}, sentNumbers(actions))
	require.Empty(t, s.Tick(now.Add(3*time.Second)))
	require.Equal(t, []uint32{12}, sentNumbers(s.Tick(now.Add(4*time.Second))))
	require.Equal(t, []uint32{20}, sentNumbers(s.Tick(now.Add(6*time.Second))))
}

func TestSenderInvalidRepairIsAtomic(t *testing.T) {
	s, now := newTestSender(t)
	_, err := s.Start(now)
	require.NoError(t, err)
	due, _ := s.NextDeadline()
	for _, nums := range [][]uint32{nil, {1, 1}, {3, 2}, {10}, {21}, {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}} {
		a, e := s.Repair(nums, now)
		require.Error(t, e)
		require.Empty(t, a)
		got, _ := s.NextDeadline()
		require.Equal(t, due, got)
	}
	require.Equal(t, []uint32{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, sentNumbers(s.Tick(now.Add(2*time.Second))))
}

func TestSenderOwnsPayloadsAndRetainsQ2(t *testing.T) {
	now := time.Unix(100, 0)
	meta := Metadata{Size: 16, Identity: []byte{1}}
	payload := bytes.Repeat([]byte{7}, 16)
	s, err := NewSender(Q2, DefaultTransferConfig(), meta, payload, now, 0)
	require.NoError(t, err)
	payload[0] = 99
	meta.Identity[0] = 2
	actions, err := s.Start(now)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	require.Equal(t, Complete, actions[1].Kind)
	require.Equal(t, byte(7), actions[0].Payload[0])
	actions[0].Payload[0] = 88
	actions, err = s.Repair([]uint32{0}, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, byte(7), actions[0].Payload[0])
	actions = s.Tick(now.Add(247 * time.Second))
	require.Len(t, actions, 1)
	require.Equal(t, Release, actions[0].Kind)
	require.Nil(t, s.payload)
	require.Empty(t, s.Tick(now.Add(248*time.Second)))
}

func TestSenderExpiryAndValidation(t *testing.T) {
	s, now := newTestSender(t)
	_, err := s.Start(now)
	require.NoError(t, err)
	actions := s.Tick(now.Add(247 * time.Second))
	require.Len(t, actions, 2)
	require.ErrorIs(t, actions[0].Err, ErrExpired)
	require.Nil(t, s.payload)
	_, err = s.Repair([]uint32{0}, now)
	require.Error(t, err)
	for _, kind := range []Kind{0, 3} {
		_, err = NewSender(kind, DefaultTransferConfig(), Metadata{}, nil, now, 0)
		require.Error(t, err)
	}
	_, err = NewSender(Q1, DefaultTransferConfig(), Metadata{Size: 1}, nil, now, 0)
	require.Error(t, err)
}

func TestSenderRepairQueueLimitAndOwnership(t *testing.T) {
	now := time.Unix(100, 0)
	cfg := DefaultTransferConfig()
	cfg.MaxPayloads = 3
	s, err := NewSender(Q1, cfg, Metadata{Size: 7 * 16}, make([]byte, 7*16), now, 0)
	require.NoError(t, err)
	_, err = s.Start(now)
	require.NoError(t, err)
	s.Tick(now.Add(2 * time.Second))
	report := []uint32{1, 3, 4}
	actions, err := s.Repair(report, now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, []uint32{1}, sentNumbers(actions))
	report[1] = 0
	deadline, _ := s.NextDeadline()
	actions, err = s.Repair([]uint32{0, 2}, now.Add(3*time.Second))
	require.ErrorIs(t, err, ErrInvalidRepair)
	require.Empty(t, actions)
	next, _ := s.NextDeadline()
	require.Equal(t, deadline, next)
	actions, err = s.Repair([]uint32{3, 4, 5}, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Empty(t, actions)
	require.Equal(t, []uint32{3, 4, 5}, sentNumbers(s.Tick(now.Add(4*time.Second))))
}

func TestSenderLateTickAndEarlyCancel(t *testing.T) {
	s, now := newTestSender(t)
	_, err := s.Start(now)
	require.NoError(t, err)
	require.Len(t, sentNumbers(s.Tick(now.Add(20*time.Second))), 10)
	deadline, ok := s.NextDeadline()
	require.True(t, ok)
	require.Equal(t, now.Add(22*time.Second), deadline)
	require.Empty(t, s.Tick(now.Add(20*time.Second)))
	actions := s.Cancel(nil)
	require.Len(t, actions, 2)
	require.ErrorIs(t, actions[0].Err, ErrCanceled)
	require.Nil(t, s.payload)
	require.Empty(t, s.Cancel(nil))
	s, now = newTestSender(t)
	actions, err = s.Start(now.Add(247 * time.Second))
	require.NoError(t, err)
	require.Len(t, actions, 2)
	require.ErrorIs(t, actions[0].Err, ErrExpired)
	require.Empty(t, sentNumbers(actions))
	require.Nil(t, s.payload)
}
