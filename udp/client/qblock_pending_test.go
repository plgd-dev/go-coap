package client

import (
	"math"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

func TestQBlockPendingSharedCapacityRollback(t *testing.T) {
	q := newQBlockWorkQueue(2, 128)
	client, err := q.reserve(48)
	require.NoError(t, err)
	server, err := q.reserve(48)
	require.NoError(t, err)
	_, err = q.reserve(1)
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	now := time.Unix(100, 0)
	require.NoError(t, q.replace(client, qblockPendingWork{Kind: qblockWorkBody, Operation: "client", Expires: now.Add(time.Minute)}, false))
	require.NoError(t, q.replace(server, qblockPendingWork{Kind: qblockWorkBody, Operation: "server", Expires: now.Add(time.Minute)}, false))
	id, original, ok := q.next(now, newQBlockProbeGate(1))
	require.True(t, ok)
	require.Equal(t, client, id)
	require.Equal(t, qblock.OperationKey("client"), original.Operation)
	bad := qblockPendingWork{Kind: qblockWorkGET, Operation: "client", RequestOptions: message.Options{{ID: message.URIPath, Value: make([]byte, 64)}}, Expires: now.Add(time.Minute)}
	require.ErrorIs(t, q.replace(client, bad, false), qblock.ErrLimitExceeded)
	id, original, ok = q.next(now, newQBlockProbeGate(1))
	require.True(t, ok)
	require.Equal(t, client, id)
	require.Equal(t, qblockWorkBody, original.Kind)
	require.Equal(t, uint64(96), q.used)
	q.release(client)
	q.release(client)
	require.Equal(t, uint64(48), q.used)
	third, err := q.reserve(80)
	require.NoError(t, err)
	require.NotEqual(t, client, third)
}

func TestQBlockPendingControlCapacitySurvivesFullQueue(t *testing.T) {
	options := message.Options{{ID: message.URIPath, Value: []byte("sensor")}}
	capacity, err := qblockControlCapacity(options, 2)
	require.NoError(t, err)
	q := newQBlockWorkQueue(1, capacity)
	id, err := q.reserve(capacity)
	require.NoError(t, err)
	now := time.Unix(100, 0)
	require.NoError(t, q.replace(id, qblockPendingWork{Kind: qblockWorkBody, Expires: now.Add(time.Minute)}, false))
	_, err = q.reserve(1)
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	require.NoError(t, q.replace(id, qblockPendingWork{
		Kind: qblockWorkControls, Expires: now.Add(time.Minute), RequestOptions: options,
		Controls: []qblockControlWork{{Intent: qblock.ControlIntent{Action: qblock.Action{Kind: qblock.SendContinue}}},
			{Intent: qblock.ControlIntent{Action: qblock.Action{Kind: qblock.RequestMissing, Numbers: []uint32{2, 3}}}}},
	}, true))
	require.Equal(t, capacity, q.used)
}

func TestQBlockPendingOrderingAndCopies(t *testing.T) {
	q := newQBlockWorkQueue(3, 4096)
	first, err := q.reserve(256)
	require.NoError(t, err)
	second, err := q.reserve(256)
	require.NoError(t, err)
	now := time.Unix(100, 0)
	opts := message.Options{{ID: message.URIPath, Value: []byte("original")}}
	token := message.Token{1, 2}
	numbers := []uint32{4, 5}
	work := qblockPendingWork{Kind: qblockWorkControls, Operation: "first", Expires: now.Add(time.Minute), RequestOptions: opts, RequestToken: token, RequestCode: codes.GET,
		Controls: []qblockControlWork{{ReplyToken: token, Intent: qblock.ControlIntent{Action: qblock.Action{Kind: qblock.RequestMissing, Numbers: numbers}}}}}
	require.NoError(t, q.replace(first, work, false))
	require.NoError(t, q.replace(second, qblockPendingWork{Kind: qblockWorkGET, Operation: "second", Expires: now.Add(time.Minute)}, false))
	opts[0].Value[0], token[0], numbers[0] = 'X', 9, 99
	id, snapshot, ok := q.next(now, newQBlockProbeGate(1))
	require.True(t, ok)
	require.Equal(t, first, id)
	require.Equal(t, []byte("original"), snapshot.RequestOptions[0].Value)
	require.Equal(t, message.Token{1, 2}, snapshot.Controls[0].ReplyToken)
	require.Equal(t, []uint32{4, 5}, snapshot.Controls[0].Intent.Action.Numbers)
	snapshot.RequestToken[0] = 8
	snapshot.Controls[0].Intent.Action.Numbers[0] = 8
	_, again, _ := q.next(now, newQBlockProbeGate(1))
	require.Equal(t, message.Token{1, 2}, again.RequestToken)
	require.Equal(t, []uint32{4, 5}, again.Controls[0].Intent.Action.Numbers)
	require.NoError(t, q.replace(first, qblockPendingWork{Kind: qblockWorkBody, Operation: "replacement", Expires: now.Add(time.Minute)}, true))
	id, _, _ = q.next(now, newQBlockProbeGate(1))
	require.Equal(t, first, id)
	require.NoError(t, q.replace(first, qblockPendingWork{Kind: qblockWorkBody, Operation: "later", Expires: now.Add(time.Minute)}, false))
	id, _, _ = q.next(now, newQBlockProbeGate(1))
	require.Equal(t, second, id)
}

func TestQBlockPendingExpiryAndUngatedContinue(t *testing.T) {
	q := newQBlockWorkQueue(3, 1024)
	first, _ := q.reserve(64)
	second, _ := q.reserve(64)
	third, _ := q.reserve(64)
	now := time.Unix(100, 0)
	require.NoError(t, q.replace(first, qblockPendingWork{Kind: qblockWorkBody, Expires: now}, false))
	require.NoError(t, q.replace(second, qblockPendingWork{Kind: qblockWorkGET, Expires: now.Add(time.Minute)}, false))
	require.NoError(t, q.replace(third, qblockPendingWork{Kind: qblockWorkControls, Expires: now.Add(time.Minute), Controls: []qblockControlWork{{Intent: qblock.ControlIntent{Action: qblock.Action{Kind: qblock.SendContinue}}}}}, false))
	gate := newQBlockProbeGate(1)
	require.True(t, gate.admit(1, qblockProbeBody, 0, now))
	id, _, ok := q.next(now, gate)
	require.True(t, ok)
	require.Equal(t, first, id)
	q.clearPending(first)
	id, _, ok = q.next(now, gate)
	require.True(t, ok)
	require.Equal(t, third, id)
	deadline, ok := q.nextDeadline(now, gate)
	require.True(t, ok)
	require.Equal(t, now, deadline)
	q.clearPending(third)
	deadline, ok = q.nextDeadline(now, gate)
	require.True(t, ok)
	require.Equal(t, now.Add(time.Minute), deadline)
	q.clearPending(second)
	_, ok = q.nextDeadline(now, gate)
	require.False(t, ok)
}

func TestQBlockPendingOversizedOptions(t *testing.T) {
	opts := message.Options{{ID: message.URIPath, Value: []byte("abcd")}}
	capacity, err := qblockControlCapacity(opts, 2)
	require.NoError(t, err)
	require.Greater(t, capacity, uint64(4))
	_, err = qblockControlCapacity(opts, math.MaxUint32)
	require.Error(t, err)
	exact := qblockPendingWork{Kind: qblockWorkGET, RequestOptions: opts}
	exactBytes, err := qblockWorkBytes(exact)
	require.NoError(t, err)
	q := newQBlockWorkQueue(1, exactBytes)
	id, err := q.reserve(exactBytes)
	require.NoError(t, err)
	require.NoError(t, q.replace(id, exact, false))
	before := q.used
	oversized := qblockPendingWork{Kind: qblockWorkGET, RequestOptions: message.Options{{ID: message.URIPath, Value: []byte("abcde")}}}
	require.ErrorIs(t, q.replace(id, oversized, false), qblock.ErrLimitExceeded)
	require.Equal(t, before, q.used)
	_, got, ok := q.next(time.Unix(100, 0), newQBlockProbeGate(1))
	require.True(t, ok)
	require.Equal(t, []byte("abcd"), got.RequestOptions[0].Value)
}
