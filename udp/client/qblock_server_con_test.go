package client

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func conGET(t *testing.T, h *serverHarness, mid int32, token byte, block uint32) *pool.Message {
	t.Helper()
	r := h.cc.AcquireMessage(context.Background())
	r.SetCode(codes.GET)
	r.SetType(message.Confirmable)
	r.SetMessageID(mid)
	r.SetToken([]byte{token})
	require.NoError(t, r.SetPath("/probe"))
	r.SetOptionUint32(message.QBlock2, block)
	return r
}

// Catches silent CON interception, whole-body sending, and incorrect response M.
func TestQBlockServerCONSingleBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		block uint32
	}{{"empty", "", 0}, {"short", "abcd", 0}, {"long", "abcdefghijklmnopqrstuvwxyz0123456789ABCD", 8}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
				require.Equal(t, message.Confirmable, r.Type())
				require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte(tc.body))))
			})
			h.ingest(conGET(t, h, 70, 1, 0))
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			got := writes[0]
			require.Equal(t, message.Acknowledgement, got.typ)
			require.EqualValues(t, 70, got.mid)
			require.Equal(t, codes.Content, got.code)
			require.Equal(t, tc.block, got.block)
			require.Equal(t, tc.body[:min(len(tc.body), 16)], string(got.payload))
			size, err := got.options.GetUint32(message.Size2)
			require.NoError(t, err)
			require.EqualValues(t, len(tc.body), size)
			etag, err := got.options.GetBytes(message.ETag)
			require.NoError(t, err)
			require.NotEmpty(t, etag)
			require.LessOrEqual(t, len(etag), 8)
			require.Zero(t, h.cc.qblockClient.manager.Active())
		})
	}
}

func TestQBlockServerCONOffsetAndETag(t *testing.T) {
	body := bytes.NewReader([]byte("abcdefghijklmnopqrstuvwxyz0123456789ABCD"))
	_, err := body.Seek(3, io.SeekStart)
	require.NoError(t, err)
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, body, message.Option{ID: message.ETag, Value: []byte("version")}))
	})
	h.ingest(conGET(t, h, 70, 1, 16))
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, []byte("qrstuvwxyz012345"), writes[0].payload)
	require.EqualValues(t, 24, writes[0].block)
	etag, err := writes[0].options.GetBytes(message.ETag)
	require.NoError(t, err)
	require.Equal(t, []byte("version"), etag)
	pos, err := body.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.EqualValues(t, 3, pos)
}

func TestQBlockServerCONDuplicates(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("abcd"))))
	})
	h.ingest(conGET(t, h, 70, 1, 0))
	h.ingest(conGET(t, h, 70, 1, 0))
	h.ingest(conGET(t, h, 70, 2, 0))
	conflict := conGET(t, h, 70, 1, 0)
	require.NoError(t, conflict.SetPath("/other"))
	h.ingest(conflict)
	require.Equal(t, 1, calls)
	writes := h.session.writesSnapshot()
	require.Len(t, writes, 2)
	require.Equal(t, writes[0], writes[1])
}

func TestQBlockServerCONNoResponse(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "SetResponse", true: "direct"}[direct], func(t *testing.T) {
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				if direct {
					w.Message().SetCode(codes.Content)
					w.Message().SetBody(bytes.NewReader([]byte("secret")))
				} else {
					require.Error(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("secret"))))
				}
			})
			r := conGET(t, h, 70, 1, 0)
			r.SetOptionUint32(message.NoResponse, 2)
			h.ingest(r)
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			require.Equal(t, codes.Empty, writes[0].code)
			require.Empty(t, writes[0].token)
			require.Empty(t, writes[0].payload)
		})
	}
}

func TestQBlockServerCONLimits(t *testing.T) {
	t.Run("shared records", func(t *testing.T) {
		calls := 0
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxRecords: 1}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			calls++
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok"))))
		})
		h.ingest(h.q1(t, 3, 0, true, 32, "abcdefghijklmnop"))
		h.ingest(conGET(t, h, 70, 1, 0))
		require.Zero(t, calls)
		writes := h.session.writesSnapshot()
		require.Len(t, writes, 1)
		require.Equal(t, codes.ServiceUnavailable, writes[0].code)
	})
	t.Run("body overflow", func(t *testing.T) {
		mc := qblock.DefaultManagerConfig()
		mc.Transfer.MaxBodySize = 32
		h := newServerHarness(t, mc, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader(make([]byte, 33))))
		})
		h.ingest(conGET(t, h, 70, 1, 0))
		writes := h.session.writesSnapshot()
		require.Len(t, writes, 1)
		require.Equal(t, codes.InternalServerError, writes[0].code)
		require.False(t, writes[0].options.HasOption(message.QBlock2))
	})
	t.Run("bad offset", func(t *testing.T) {
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("ok"))))
		})
		h.ingest(conGET(t, h, 70, 1, 16))
		writes := h.session.writesSnapshot()
		require.Len(t, writes, 1)
		require.Equal(t, codes.BadRequest, writes[0].code)
	})
}

func TestQBlockServerCONMetadataAndDatagramLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata uint64
		value    int
	}{{"metadata", 64, 100}, {"datagram", 4096, 2100}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxMetadataBytes: tc.metadata}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				require.NoError(t, w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("x")), message.Option{ID: message.LocationPath, Value: bytes.Repeat([]byte{'m'}, tc.value)}))
			})
			h.ingest(conGET(t, h, 70, 1, 0))
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			require.NotEqual(t, codes.Content, writes[0].code)
			require.False(t, writes[0].options.HasOption(message.QBlock2))
		})
	}
}
func TestQBlockServerCONMalformed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block uint32
	}{{"reserved SZX", 7}, {"full body unsupported", 8}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(*responsewriter.ResponseWriter[*Conn], *pool.Message) { calls++ })
			h.ingest(conGET(t, h, 70, 1, tc.block))
			writes := h.session.writesSnapshot()
			require.Len(t, writes, 1)
			require.Equal(t, codes.BadOption, writes[0].code)
			require.Zero(t, calls)
		})
	}
}
