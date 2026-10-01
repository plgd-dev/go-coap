package client

import (
	"bytes"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func q2SelectorHarness(t *testing.T, blocks int) (*serverHarness, *int) {
	t.Helper()
	body := make([]byte, blocks*16)
	for n := 0; n < blocks; n++ {
		copy(body[n*16:], bytes.Repeat([]byte{byte(n)}, 16))
	}
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(body)))
	})
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	return h, &calls
}

func q2Selectors(t *testing.T, h *serverHarness, raw ...uint32) *pool.Message {
	t.Helper()
	m := h.control(t, 11, 0, false, "tag-a")
	m.Remove(message.QBlock2)
	for _, v := range raw {
		m.AddOptionUint32(message.QBlock2, v)
	}
	return m
}

// Treating non-boundary M1 as Continue, rejecting repeated options, or
// expanding overlapping selections more than once loses valid repair traffic.
func TestQBlockServerQ2Selectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []uint32
		want []uint32
	}{
		{"suffix", []uint32{0x28}, []uint32{2, 3, 4, 5, 6, 7, 8, 9}},
		{"individuals", []uint32{0x20, 0x40}, []uint32{2, 4}},
		{"overlap", []uint32{0x28, 0x40}, []uint32{2, 3, 4, 5, 6, 7, 8, 9}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, calls := q2SelectorHarness(t, 13)
			before := h.session.writesSnapshot()
			require.Len(t, before, 10)
			h.ingest(q2Selectors(t, h, tc.raw...))
			got := h.session.writesSnapshot()[len(before):]
			require.Len(t, got, len(tc.want))
			mids := make(map[int32]bool)
			for i, w := range got {
				v, e := w.options.GetUint32(message.QBlock2)
				require.NoError(t, e)
				require.Equal(t, tc.want[i], v>>4)
				require.Equal(t, message.Token{11}, w.token)
				require.Equal(t, message.NonConfirmable, w.typ)
				require.Equal(t, bytes.Repeat([]byte{byte(tc.want[i])}, 16), w.payload)
				etag, e := w.options.GetBytes(message.ETag)
				require.NoError(t, e)
				originalETag, e := before[0].options.GetBytes(message.ETag)
				require.NoError(t, e)
				require.Equal(t, originalETag, etag)
				size, e := w.options.GetUint32(message.Size2)
				require.NoError(t, e)
				require.EqualValues(t, 208, size)
				require.False(t, mids[w.mid])
				mids[w.mid] = true
			}
			require.Equal(t, 1, *calls)
		})
	}
}

// A repeated request spanning sets must remain one accepted control, while
// the existing sender emits only one set of repairs at each deadline.
func TestQBlockServerQ2SelectorsCrossSets(t *testing.T) {
	h, calls := q2SelectorHarness(t, 13)
	h.advance(2 * time.Second)
	before := len(h.session.writesSnapshot())
	h.ingest(q2Selectors(t, h, 0x90, 0xa8, 0xc0))
	writes := h.session.writesSnapshot()
	require.Len(t, writes, before+1)
	v, e := writes[before].options.GetUint32(message.QBlock2)
	require.NoError(t, e)
	require.EqualValues(t, 9, v>>4)
	h.advance(2*time.Second - time.Nanosecond)
	require.Len(t, h.session.writesSnapshot(), before+1)
	h.advance(time.Nanosecond)
	writes = h.session.writesSnapshot()
	require.Len(t, writes, before+4)
	for i, w := range writes[before:] {
		v, e := w.options.GetUint32(message.QBlock2)
		require.NoError(t, e)
		require.EqualValues(t, []uint32{9, 10, 11, 12}[i], v>>4)
		require.Equal(t, message.Token{11}, w.token)
	}
	require.Equal(t, 1, *calls)
}

// Invalid suffix expansion must be rejected before a token, queue entry,
// response, or body mutation becomes observable; the next valid repair works.
func TestQBlockServerQ2SelectorsRejectAtomically(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []uint32
	}{
		{"descending", []uint32{0x40, 0x20}},
		{"duplicate_num", []uint32{0x28, 0x20}},
		{"mixed_szx", []uint32{0x20, 0x41}},
		{"past_body", []uint32{0x20, 0xd0}},
		{"unsent", []uint32{0x20, 0xa0}},
		{"expanded_cap", []uint32{0x08, 0xa8}},
		{"whole_body_cap", []uint32{0x08, 0x40}},
		{"illegal_szx", []uint32{0x20, 0x47}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := q2SelectorHarness(t, 13)
			before := h.snapshot()
			h.cc.qblockClient.mu.Lock()
			deadline, ok := h.cc.qblockClient.manager.NextDeadline()
			h.cc.qblockClient.mu.Unlock()
			h.ingest(q2Selectors(t, h, tc.raw...))
			require.Equal(t, before, h.snapshot())
			h.cc.qblockClient.mu.Lock()
			after, afterOK := h.cc.qblockClient.manager.NextDeadline()
			h.cc.qblockClient.mu.Unlock()
			require.Equal(t, ok, afterOK)
			require.Equal(t, deadline, after)
			require.False(t, h.serverTokenBound(11))
			beforeWrites := len(h.session.writesSnapshot())
			h.ingest(q2Selectors(t, h, 0x20))
			writes := h.session.writesSnapshot()
			require.Len(t, writes, beforeWrites+1)
			require.Equal(t, message.Token{11}, writes[len(writes)-1].token)
		})
	}
}

// A raw peer may use an empty token for initial GET and subsequent repairs.
// Empty ownership must collide with another operation until the first releases.
func TestQBlockServerQ2EmptyToken(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		calls++
		require.Empty(t, r.Token())
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	})
	request := func(number uint32, more bool, tag string) *pool.Message {
		m := h.control(t, 11, number, more, tag)
		m.SetCode(codes.GET)
		m.SetToken(nil)
		return m
	}
	h.ingest(request(0, true, "tag-a"))
	require.Equal(t, 1, calls)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 3)
	for _, w := range writes {
		require.Empty(t, w.token)
	}
	h.ingest(request(1, false, "tag-a"))
	writes = h.session.writesSnapshot()
	require.Len(t, writes, 4)
	require.Empty(t, writes[3].token)
	h.ingest(request(0, true, "tag-b"))
	require.Equal(t, 1, calls, "another operation cannot steal the empty token")
	h.ingest(request(0, true, "tag-a"))
	require.Equal(t, 1, calls, "duplicate initial GET cannot reinvoke handler")
}

func TestQBlockEmptyTokenReservationAndFreshAllocation(t *testing.T) {
	h, _ := q2SelectorHarness(t, 1)
	require.NoError(t, h.cc.claimToken(nil, tokenOwnerQBlock))
	require.Error(t, h.cc.claimToken(message.Token{}, tokenOwnerQBlock))
	h.cc.releaseToken(nil, tokenOwnerRequest)
	require.Error(t, h.cc.claimToken(nil, tokenOwnerQBlock))
	h.cc.releaseToken(nil, tokenOwnerQBlock)
	require.NoError(t, h.cc.claimToken(nil, tokenOwnerQBlock))
	h.cc.releaseToken(nil, tokenOwnerQBlock)
	require.Error(t, h.cc.claimToken(nil, tokenOwnerRequest))
	calls := 0
	h.cc.getToken = func() (message.Token, error) { calls++; return nil, nil }
	token, err := h.cc.claimFreshQBlockToken()
	require.Error(t, err)
	require.Empty(t, token)
	require.Equal(t, 32, calls)
}
