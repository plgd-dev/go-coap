package client

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
)

type serverHarness struct {
	cc      *Conn
	session *qblockTestSession
	now     time.Time
	nextMID int32
}

type serverSnapshot struct {
	active        uint32
	managerTokens int
	managerBytes  uint64
	records       int
	serverTokens  int
	mids          int
	reservations  int
	metadataBytes uint64
}

func newServerHarness(t *testing.T, mc qblock.ManagerConfig, sc qblockServerConfig, handler HandlerFunc) *serverHarness {
	t.Helper()
	h := &serverHarness{now: time.Unix(100, 0), nextMID: 1}
	h.session = &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.Handler = handler
	cfg.GetMID = func() int32 {
		mid := h.nextMID
		h.nextMID++
		return mid
	}
	h.cc = NewConnWithOpts(h.session, &cfg,
		withQBlockClient(qblockClientConfig{Manager: mc, Now: func() time.Time { return h.now }}),
		withQBlockServer(sc),
	)
	t.Cleanup(h.session.closeForTest)
	return h
}

func (h *serverHarness) q1(t *testing.T, token byte, number uint32, more bool, size uint32, body string) *pool.Message {
	t.Helper()
	msg := h.cc.AcquireMessage(context.Background())
	msg.SetType(message.NonConfirmable)
	msg.SetCode(codes.POST)
	msg.SetToken(message.Token{token})
	msg.SetMessageID(h.nextMID)
	h.nextMID++
	require.NoError(t, msg.SetPath("/upload"))
	msg.SetContentFormat(message.TextPlain)
	msg.AddOptionBytes(message.RequestTag, []byte("tag-a"))
	msg.SetOptionUint32(message.Size1, size)
	value, err := qblock.EncodeBlock(qblock.Block{Number: number, More: more, SZX: blockwise.SZX16})
	require.NoError(t, err)
	msg.SetOptionUint32(message.QBlock1, value)
	msg.SetBody(bytes.NewReader([]byte(body)))
	return msg
}

func (h *serverHarness) ingest(msg *pool.Message) {
	h.cc.ProcessReceivedMessageWithHandler(msg, h.cc.handleReq)
}

func (h *serverHarness) snapshot() serverSnapshot {
	client := h.cc.qblockClient
	client.mu.Lock()
	defer client.mu.Unlock()
	snapshot := serverSnapshot{active: client.manager.Active()}
	manager := reflect.ValueOf(client.manager).Elem()
	snapshot.managerTokens = manager.FieldByName("byToken").Len()
	snapshot.managerBytes = manager.FieldByName("retained").Uint()
	if client.server != nil {
		snapshot.records = len(client.server.records)
		snapshot.metadataBytes = client.server.metadata
		for _, record := range client.server.records {
			snapshot.serverTokens += len(record.tokens)
		}
	}
	h.cc.tokenReservations.Range(func(_ uint64, _ tokenReservation) bool {
		snapshot.reservations++
		return true
	})
	return snapshot
}

func (h *serverHarness) advance(duration time.Duration) {
	h.now = h.now.Add(duration)
	h.cc.CheckExpirations(h.now)
}

func TestQBlockServerAdmission(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))

	snapshot := h.snapshot()
	require.Equal(t, uint32(1), snapshot.active)
	require.Equal(t, 1, snapshot.records)
	require.Empty(t, h.session.writesSnapshot())
}

func TestQBlockServerRejectsMalformedFirstFragmentWithoutState(t *testing.T) {
	for name, mutate := range map[string]func(*pool.Message){
		"missing request tag": func(msg *pool.Message) { msg.Remove(message.RequestTag) },
		"missing size":        func(msg *pool.Message) { msg.Remove(message.Size1) },
		"mixed classic block": func(msg *pool.Message) { msg.SetOptionUint32(message.Block1, 0) },
		"short payload": func(msg *pool.Message) {
			msg.SetBody(bytes.NewReader([]byte("short")))
		},
		"empty token": func(msg *pool.Message) { msg.SetToken(nil) },
		"oversized identity": func(msg *pool.Message) {
			msg.AddOptionBytes(message.URIQuery, bytes.Repeat([]byte{'x'}, 512))
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
			before := h.snapshot()
			msg := h.q1(t, 1, 0, true, 32, "abcdefghijklmnop")
			mutate(msg)

			h.ingest(msg)

			require.Equal(t, before, h.snapshot())
			require.Empty(t, h.session.writesSnapshot())
		})
	}
}

func TestQBlockServerRetainsDeliveredQ1ForLaterDispatch(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))

	h.cc.qblockClient.mu.Lock()
	require.Len(t, h.cc.qblockClient.server.records, 1)
	for _, record := range h.cc.qblockClient.server.records {
		require.True(t, record.ready)
		require.Equal(t, []byte("body"), record.payload)
	}
	h.cc.qblockClient.mu.Unlock()
	require.Equal(t, uint32(1), h.snapshot().active)
	require.Empty(t, h.session.writesSnapshot())
}

func TestQBlockServerSendsContinueAtQ1SetBoundary(t *testing.T) {
	mc := qblock.DefaultManagerConfig()
	mc.Transfer.MaxPayloads = 2
	h := newServerHarness(t, mc, qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, true, 48, "abcdefghijklmnop"))
	h.ingest(h.q1(t, 2, 1, true, 48, "qrstuvwxyzabcdef"))

	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.Continue, writes[0].code)
	require.Equal(t, message.Token{2}, writes[0].token)
	value, err := writes[0].options.GetUint32(message.QBlock1)
	require.NoError(t, err)
	block, err := qblock.DecodeBlock(value)
	require.NoError(t, err)
	require.Equal(t, uint32(1), block.Number)
	require.True(t, block.More)
}

func TestQBlockServerRequestsMissingBlocksOnTick(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 1, false, 32, "qrstuvwxyzabcdef"))
	h.advance(qblock.DefaultTransferConfig().NonReceiveTimeout)

	writes := h.session.writesSnapshot()
	require.Len(t, writes, 1)
	require.Equal(t, codes.RequestEntityIncomplete, writes[0].code)
	require.Equal(t, message.Token{1}, writes[0].token)
	require.Equal(t, message.AppMissingBlocksCBORSeq, mustContentFormat(t, writes[0].options))
	missing, err := qblock.DecodeMissing(writes[0].payload, 2, 2)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, missing)
}

func TestQBlockServerAdmissionDoesNotStealOrdinaryToken(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	require.NoError(t, h.cc.claimToken(message.Token{1}, tokenOwnerRequest))
	defer h.cc.releaseToken(message.Token{1}, tokenOwnerRequest)
	before := h.snapshot()

	h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))

	require.Equal(t, before, h.snapshot())
}

func TestQBlockServerRecordLimitRefusesNewIdentity(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxRecords: 1}, nil)
	h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))
	before := h.snapshot()
	second := h.q1(t, 2, 0, true, 32, "abcdefghijklmnop")
	require.NoError(t, second.SetPath("/other"))

	h.ingest(second)

	require.Equal(t, before, h.snapshot())
}

func TestQBlockServerRecoveryWriteFailureReleasesAdmissionState(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	before := h.snapshot()
	h.session.writeErr = errors.New("write failed")
	h.ingest(h.q1(t, 1, 1, false, 32, "qrstuvwxyzabcdef"))

	h.advance(qblock.DefaultTransferConfig().NonReceiveTimeout)

	require.Equal(t, before, h.snapshot())
}

func TestQBlockServerCloseReleasesReadyReceiver(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	require.Equal(t, uint32(1), h.snapshot().active)

	h.session.closeForTest()

	require.Equal(t, serverSnapshot{}, h.snapshot())
}

func TestQBlockServerMetadataConflictCancelsOnlyResolvedReceiver(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))
	conflict := h.q1(t, 2, 1, true, 48, "qrstuvwxyzabcdef")
	_, options, err := serverQ1Fragment(conflict)
	require.NoError(t, err)
	operation, err := serverRequestKey(codes.POST, options)
	require.NoError(t, err)
	for existing := range h.cc.qblockClient.server.records {
		require.Equal(t, existing, operation)
	}
	conflict.SetBody(bytes.NewReader([]byte("qrstuvwxyzabcdef")))

	h.ingest(conflict)

	require.Equal(t, serverSnapshot{}, h.snapshot())
}

func TestQBlockServerSameTagDifferentRequestIdentityAdmitsBoth(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))
	other := h.q1(t, 2, 0, true, 32, "abcdefghijklmnop")
	other.AddOptionString(message.URIQuery, "other=true")

	h.ingest(other)

	require.Equal(t, uint32(2), h.snapshot().active)
	require.Equal(t, 2, h.snapshot().records)
}

func TestQBlockServerOutOfOrderFirstBlockAdmitsReceiver(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	h.ingest(h.q1(t, 1, 1, false, 32, "qrstuvwxyzabcdef"))

	require.Equal(t, uint32(1), h.snapshot().active)
	require.Empty(t, h.session.writesSnapshot())
}

func TestQBlockServerAdmissionLimitsRollback(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxMetadataBytes: 1}, nil)
		before := h.snapshot()
		h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))
		require.Equal(t, before, h.snapshot())
	})
	t.Run("manager bytes", func(t *testing.T) {
		mc := qblock.DefaultManagerConfig()
		mc.Transfer.MaxBodySize = 32
		mc.MaxRetainedBytes = 32
		h := newServerHarness(t, mc, qblockServerConfig{}, nil)
		before := h.snapshot()
		h.ingest(h.q1(t, 1, 0, true, 48, "abcdefghijklmnop"))
		require.Equal(t, before, h.snapshot())
	})
	t.Run("manager transfers", func(t *testing.T) {
		mc := qblock.DefaultManagerConfig()
		mc.MaxTransfers, mc.MaxTokens = 1, 2
		h := newServerHarness(t, mc, qblockServerConfig{MaxRecords: 2}, nil)
		h.ingest(h.q1(t, 1, 0, true, 32, "abcdefghijklmnop"))
		before := h.snapshot()
		second := h.q1(t, 2, 0, true, 32, "abcdefghijklmnop")
		require.NoError(t, second.SetPath("/other"))
		h.ingest(second)
		require.Equal(t, before, h.snapshot())
	})
}

func TestQBlockServerRequiresValidPrivateConstruction(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	clientOnly := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig()}))
	require.Nil(t, clientOnly.qblockClient.server)

	invalid := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg,
		withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig()}),
		withQBlockServer(qblockServerConfig{Retention: -time.Second}),
	)
	require.Nil(t, invalid.qblockClient.server)
}

func mustContentFormat(t *testing.T, options message.Options) message.MediaType {
	t.Helper()
	format, err := options.ContentFormat()
	require.NoError(t, err)
	return format
}
