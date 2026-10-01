package qblocklink

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// Literal datagrams keep the oracle independent of the production codec.
var q1 = []byte{0x50, 2, 0, 1, 0xd0, 6}  // NON POST, Q1=0.
var q2 = []byte{0x50, 1, 0, 2, 0xd0, 18} // NON GET, Q2=0.
func relay(t *testing.T, r []Rule) *Link {
	t.Helper()
	l, e := New(r, Limits{100, 4096})
	require.NoError(t, e)
	return l
}

// Wrong direction/kind counters or replaying a dropped packet must fail.
func TestSelection(t *testing.T) {
	l := relay(t, []Rule{{ClientToServer, Q1, 2, Drop}, {ServerToClient, Q2, 1, Duplicate}})
	a, e := l.Process(ClientToServer, q1)
	require.NoError(t, e)
	require.Len(t, a, 1)
	b, e := l.Process(ServerToClient, q1)
	require.NoError(t, e)
	require.Len(t, b, 1)
	c, e := l.Process(ClientToServer, q2)
	require.NoError(t, e)
	require.Len(t, c, 1)
	d, e := l.Process(ClientToServer, q1)
	require.NoError(t, e)
	require.Empty(t, d)
	f, e := l.Process(ServerToClient, q2)
	require.NoError(t, e)
	require.Len(t, f, 2)
	require.Equal(t, q2, f[0].Wire)
	require.Equal(t, q2, f[1].Wire)
	tr := l.Trace()
	require.Len(t, tr, 5)
	require.Equal(t, uint64(2), tr[3].Occurrence)
	require.Equal(t, Drop, tr[3].Action)
}

// Release order must reorder held packets without applying rules again.
func TestHoldReorderAtomicRelease(t *testing.T) {
	l := relay(t, []Rule{{ClientToServer, Q1, 1, Hold}, {ClientToServer, Q1, 2, Hold}})
	a, e := l.Process(ClientToServer, q1)
	require.NoError(t, e)
	require.Empty(t, a)
	other := append([]byte(nil), q1...)
	other[3] = 9
	a, e = l.Process(ClientToServer, other)
	require.NoError(t, e)
	require.Empty(t, a)
	_, e = l.Release(2, 99)
	require.Error(t, e)
	require.Len(t, l.Trace(), 2)
	_, e = l.Release(1, 1)
	require.Error(t, e)
	a, e = l.Release(2, 1)
	require.NoError(t, e)
	require.Len(t, a, 2)
	require.Equal(t, other, a[0].Wire)
	require.Equal(t, q1, a[1].Wire)
	tr := l.Trace()
	require.Len(t, tr, 4)
	require.Equal(t, Released, tr[2].Action)
	require.Equal(t, uint64(2), tr[2].ID)
	_, e = l.Release(1)
	require.Error(t, e)
	a, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	require.Len(t, a, 1)
	require.Equal(t, uint64(3), l.Trace()[4].Occurrence)
}

// Classifier must not silently lose malformed Q or treat controls as data.
func TestRawClassification(t *testing.T) {
	cases := []struct {
		wire []byte
		kind Kind
	}{
		{q1, Q1}, {q2, Q2},
		{[]byte{0x50, 1, 0, 1, 0xe0, 0, 1}, Ordinary},   // option270, two-byte delta
		{[]byte{0x50, 1, 0, 1, 0xe0, 0}, Malformed},     // truncated two-byte delta
		{[]byte{0x50, 1, 0, 1, 0x1e, 0}, Malformed},     // truncated two-byte length {[]byte{0x50, 95, 0, 1, 0xd0, 6}, Continue},
		{[]byte{0x50, 136, 0, 1, 0xc2, 1, 16}, Missing}, // Content-Format272
		{[]byte{0x60, 0, 0, 1}, ACK}, {[]byte{0x70, 0, 0, 1}, Reset},
		{[]byte{0x50, 1, 0, 1}, Ordinary}, {[]byte{0x50, 1, 0, 1, 0xd4, 18, 0, 0, 0, 0}, Malformed},
		{[]byte{0x50, 1, 0, 1, 0xd1, 18, 7}, Malformed}, // SZX7
		{[]byte{0x50, 1, 0, 1, 0xf0}, Malformed}, {[]byte{0x50, 1}, Malformed},
		{[]byte{0x50, 1, 0, 1, 0xd0, 18, 0}, Q2}, // repeated Q2
	}
	for _, c := range cases {
		l := relay(t, nil)
		_, e := l.Process(ClientToServer, c.wire)
		require.NoError(t, e)
		require.Len(t, l.Trace(), 1)
		require.Equal(t, c.kind, l.Trace()[0].Kind, "wire %x", c.wire)
	}
}

// Aliasing input/output/rules/trace must not corrupt held bytes or evidence.
func TestDetachedOwnership(t *testing.T) {
	rules := []Rule{{ClientToServer, Q1, 1, Duplicate}, {ClientToServer, Q1, 2, Hold}}
	l := relay(t, rules)
	rules[0].Action = Drop
	w := append([]byte(nil), q1...)
	a, e := l.Process(ClientToServer, w)
	require.NoError(t, e)
	require.Len(t, a, 2)
	w[0] = 0
	a[0].Wire[0] = 0
	require.Equal(t, q1, a[1].Wire)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	tr := l.Trace()
	tr[0].Wire[0] = 0
	tr[1].Wire[0] = 0
	tr[1].ID = 99
	a, e = l.Release(2)
	require.NoError(t, e)
	require.Equal(t, q1, a[0].Wire)
	a[0].Wire[0] = 0
	require.Equal(t, q1, l.Trace()[0].Wire)
	require.Equal(t, q1, l.Trace()[2].Wire)
}

// Limits/invalid events must fail atomically, preserving counters and holds.
func TestValidationAndLimits(t *testing.T) {
	for _, r := range [][]Rule{{{Direction: 2, Kind: Q1, Occurrence: 1, Action: Drop}}, {{ClientToServer, Q1, 0, Drop}}, {{ClientToServer, "bad", 1, Drop}}, {{ClientToServer, Q1, 1, Released}}, {{ClientToServer, Q1, 1, Drop}, {ClientToServer, Q1, 1, Hold}}} {
		_, e := New(r, Limits{10, 100})
		require.Error(t, e)
	}
	for _, lim := range []Limits{{0, 1}, {1, 0}, {-1, 1}} {
		_, e := New(nil, lim)
		require.Error(t, e)
	}
	l, e := New([]Rule{{ClientToServer, Q1, 1, Hold}}, Limits{2, len(q1) * 2})
	require.NoError(t, e)
	_, e = l.Process(2, q1)
	require.Error(t, e)
	require.Empty(t, l.Trace())
	_, e = l.Process(ClientToServer, make([]byte, 100))
	require.Error(t, e)
	require.Empty(t, l.Trace())
	a, e := l.Process(ClientToServer, q1)
	require.NoError(t, e)
	require.Empty(t, a)
	require.Equal(t, uint64(1), l.Trace()[0].ID)
	a, e = l.Release(1)
	require.NoError(t, e)
	require.Len(t, a, 1)
	_, e = l.Process(ClientToServer, q1)
	require.Error(t, e)
	require.Len(t, l.Trace(), 2)
	l, e = New([]Rule{{ClientToServer, Q1, 1, Hold}}, Limits{3, len(q1)})
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	_, e = l.Release(1)
	require.Error(t, e)
	require.Len(t, l.Trace(), 1)
}

// Removing the event cap or consuming holds on a rejected release must fail.
func TestEventLimitAndRejectedReleasePreservesHolds(t *testing.T) {
	l, e := New(nil, Limits{1, 4096})
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.Error(t, e)
	require.Len(t, l.Trace(), 1)
	l, e = New([]Rule{{ClientToServer, Q1, 1, Hold}, {ClientToServer, Q1, 2, Hold}}, Limits{3, 4096})
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	_, e = l.Release(2, 1)
	require.Error(t, e)
	require.Len(t, l.Trace(), 2)
	out, e := l.Release(1)
	require.NoError(t, e)
	require.Len(t, out, 1)
	require.Equal(t, uint64(1), out[0].ID)
	require.Contains(t, l.held, uint64(2))
	l, e = New([]Rule{{ClientToServer, Q1, 1, Hold}, {ClientToServer, Ordinary, 1, Hold}}, Limits{10, 2*len(q1) + 4})
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, q1)
	require.NoError(t, e)
	_, e = l.Process(ClientToServer, []byte{0x50, 1, 0, 2})
	require.NoError(t, e)
	_, e = l.Release(1, 2)
	require.Error(t, e)
	require.Len(t, l.Trace(), 2)
	out, e = l.Release(1)
	require.NoError(t, e)
	require.Len(t, out, 1)
	require.Equal(t, q1, out[0].Wire)
	require.Contains(t, l.held, uint64(2))
}
