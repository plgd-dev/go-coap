package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type qblockCountingReader struct {
	*bytes.Reader
	readBytes int
	failure   error
}

func (r *qblockCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.readBytes += n
	if r.failure != nil {
		return n, r.failure
	}
	return n, err
}

func TestQBlockMemoryUploadReadStopsAtBodyLimit(t *testing.T) {
	mc := qblock.DefaultManagerConfig()
	mc.Transfer.MaxBodySize, mc.MaxRetainedBytes = 16, 16
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable, cfg.BlockwiseSZX = false, blockwise.SZX16
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: mc, ScheduleMode: qblockScheduleManual}))
	t.Cleanup(session.closeForTest)
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	r := &qblockCountingReader{Reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, 4096))}
	_, err := r.Seek(7, io.SeekStart)
	require.NoError(t, err)
	req.SetBody(r)
	_, err = cc.qblockClient.prepare(req, nil)
	require.Error(t, err)
	require.LessOrEqual(t, r.readBytes, 17, "read only the cap and one overflow byte")
	position, err := r.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(7), position)
	requireQBlockClientEmpty(t, cc)
	require.Empty(t, session.writesSnapshot())
}

func TestQBlockMemoryHandlerCaptureRejectsOverflowAndReadError(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "overflow", true: "read error"}[fail], func(t *testing.T) {
			mc := qblock.DefaultManagerConfig()
			mc.Transfer.MaxBodySize = 16
			r := &qblockCountingReader{Reader: bytes.NewReader(bytes.Repeat([]byte{'r'}, 4096))}
			if fail {
				r.Reader = bytes.NewReader([]byte("partial"))
				r.failure = errors.New("body read failed")
			}
			calls := 0
			h := newServerHarness(t, mc, qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				calls++
				require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, r))
			})
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			h.ingest(h.q1(t, 2, 0, false, 4, "body"))
			require.Equal(t, 1, calls)
			require.LessOrEqual(t, r.readBytes, 17)
			require.Empty(t, h.session.writesSnapshot(), "never send a partial response after a failed capture")
			snapshot := h.snapshot()
			require.Zero(t, snapshot.active)
			require.Zero(t, snapshot.managerTokens)
			require.Zero(t, snapshot.managerBytes)
			require.Zero(t, snapshot.reservations)
			require.Empty(t, h.cc.qblockClient.workQueue.slots)
			require.Equal(t, 1, snapshot.records)
		})
	}
}

func TestQBlockMemoryInboundFragmentReadsAreBlockBounded(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	for _, q1 := range []bool{true, false} {
		t.Run(map[bool]string{true: "Q1", false: "Q2"}[q1], func(t *testing.T) {
			msg := newQBlockClientFragment(t, cc, message.Token{1}, 0, false, 16)
			defer cc.ReleaseMessage(msg)
			if q1 {
				msg.Remove(message.QBlock2)
				msg.SetCode(codes.POST)
				msg.SetOptionUint32(message.QBlock1, 0)
				msg.SetOptionUint32(message.Size1, 16)
				msg.SetOptionBytes(message.RequestTag, []byte{1})
			}
			r := &qblockCountingReader{Reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, 4096))}
			msg.SetBody(r)
			var err error
			if q1 {
				_, _, err = serverQ1Fragment(msg)
			} else {
				_, _, err = fragmentFromQ2(msg, "test", nil)
			}
			require.Error(t, err)
			require.LessOrEqual(t, r.readBytes, 17)
		})
	}
}

func TestQBlockMemoryBodyCopyExactLimitAndReadFailure(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		failure error
		wantErr bool
	}{
		{name: "empty"},
		{name: "exact", body: "0123456789abcdef"},
		{name: "overflow", body: "0123456789abcdefg", wantErr: true},
		{name: "read error", body: "partial", failure: errors.New("read failed"), wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &qblockCountingReader{Reader: bytes.NewReader([]byte(tt.body)), failure: tt.failure}
			_, err := r.Seek(2, io.SeekStart)
			require.NoError(t, err)
			payload, err := copyQBlockBody(r, 16)
			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, payload)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.body, string(payload))
			}
			pos, err := r.Seek(0, io.SeekCurrent)
			require.NoError(t, err)
			require.Equal(t, int64(2), pos)
		})
	}
}

func TestQBlockMemoryCompletedServerRecordDropsUploadBody(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
	})
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	require.Len(t, h.session.writesSnapshot(), 1)
	for _, record := range h.cc.qblockClient.server.records {
		require.Nil(t, record.payload, "duplicate suppression and Q2 repairs need identity and response, not the assembled upload")
	}
}

func TestQBlockMemoryServerResponseOptionsBudget(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact fit", true: "over budget"}[reject], func(t *testing.T) {
			var h *serverHarness
			var baseline uint64
			// Content-Format with empty value and URI-Query with 8 bytes.
			responseBytes := 2*uint64(unsafe.Sizeof(message.Option{})) + 8
			calls := 0
			h = newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
				calls++
				baseline = h.snapshot().metadataBytes
				h.cc.qblockClient.server.config.MaxMetadataBytes = baseline + responseBytes
				if reject {
					h.cc.qblockClient.server.config.MaxMetadataBytes--
				}
				require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
				w.Message().SetOptionString(message.URIQuery, "12345678")
			})
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			snapshot := h.snapshot()
			if reject {
				require.Empty(t, h.session.writesSnapshot())
				require.Zero(t, snapshot.active)
				require.Equal(t, baseline, snapshot.metadataBytes)
				require.Empty(t, h.cc.qblockClient.workQueue.slots)
			} else {
				require.Len(t, h.session.writesSnapshot(), 1)
				require.Equal(t, baseline+responseBytes, snapshot.metadataBytes)
				for _, record := range h.cc.qblockClient.server.records {
					value, err := record.responseOptions.GetBytes(message.URIQuery)
					require.NoError(t, err)
					require.Equal(t, 8, cap(value), "retained option snapshot must not pin the default 64-byte clone buffer")
					h.cc.qblockClient.server.handleReset(recordMIDForMemoryTest(record))
				}
				require.Equal(t, baseline, h.snapshot().metadataBytes)
			}
			h.ingest(h.q1(t, 2, 0, false, 4, "body"))
			require.Equal(t, 1, calls)
		})
	}
}

func recordMIDForMemoryTest(record *qblockServerRecord) int32 {
	for mid := range record.mids {
		return mid
	}
	return -1
}

func TestQBlockMemoryClientSnapshotReservation(t *testing.T) {
	for _, post := range []bool{false, true} {
		t.Run(map[bool]string{false: "GET", true: "POST"}[post], func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cfg := DefaultConfig
			cfg.BlockwiseEnable, cfg.BlockwiseSZX = false, blockwise.SZX16
			tag := message.Token{7, 8, 9}
			cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), ScheduleMode: qblockScheduleManual, GetRequestTag: func() (message.Token, error) { return tag, nil }}))
			t.Cleanup(session.closeForTest)
			req := newPrivateQBlockClientGET(t, cc, message.Token{1, 2, 3})
			if post {
				cc.ReleaseMessage(req)
				req = newPOSTWithBody(t, cc, message.Token{1, 2, 3}, []byte("body"))
			}
			defer cc.ReleaseMessage(req)
			capacity, err := qblockControlCapacity(req.Options(), 10)
			require.NoError(t, err)
			// GET preparation adds its QBlock2 advertisement before budgeting.
			if !post {
				capacity += uint64(unsafe.Sizeof(message.Option{})) + 1
			}
			cc.qblockClient.workQueue.maxBytes = capacity
			_, err = cc.qblockClient.prepare(req, nil)
			require.ErrorIs(t, err, qblock.ErrLimitExceeded)
			require.Empty(t, cc.qblockClient.workQueue.slots)
			require.Empty(t, session.writesSnapshot())
			requireQBlockClientEmpty(t, cc)
			// Restore the caller request shape after rejected GET preparation.
			req.Remove(message.QBlock2)
			cc.qblockClient.workQueue.maxBytes = 1 << 20
			prepared, err := cc.qblockClient.prepare(req, nil)
			require.NoError(t, err)
			require.True(t, prepared.Prepared)
			exchange := cc.qblockClient.exchangesByOriginalToken[string(req.Token())]
			require.NotNil(t, exchange)
			require.Equal(t, len(exchange.originalToken), cap(exchange.originalToken), "retained original token must match its byte charge")
			if post {
				require.Equal(t, len(exchange.requestTag), cap(exchange.requestTag), "retained Request-Tag must match its byte charge")
				tag[0] = 0
				require.Equal(t, []byte{7, 8, 9}, exchange.requestTag)
			}
			req.SetToken(message.Token{9, 9, 9})
			require.Equal(t, message.Token{1, 2, 3}, exchange.originalToken)
			req.SetToken(exchange.originalToken)
			value, err := exchange.requestOpts.GetBytes(message.URIPath)
			require.NoError(t, err)
			require.Equal(t, len(value), cap(value))
			before := bytes.Clone(value)
			req.SetOptionString(message.URIPath, "mutated")
			require.Equal(t, before, value)
			cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
			require.Zero(t, cc.qblockClient.workQueue.used)
		})
	}
}

func TestQBlockMemoryPendingBackingMatchesCharge(t *testing.T) {
	q := newQBlockWorkQueue(1, 4096)
	id, err := q.reserve(1)
	require.NoError(t, err)
	work := qblockPendingWork{
		Kind:           qblockWorkControls,
		RequestOptions: message.Options{{ID: message.URIPath, Value: []byte("abc")}},
		RequestToken:   message.Token{1, 2, 3},
		Controls: []qblockControlWork{{
			ReplyToken: message.Token{4, 5, 6},
			Intent:     qblock.ControlIntent{Action: qblock.Action{Numbers: []uint32{1, 2, 3}}},
		}},
	}
	require.NoError(t, q.replace(id, work, false))
	charged, err := qblockWorkBytes(work)
	require.NoError(t, err)
	require.Equal(t, charged, q.used)
	stored := q.slots[id].pending
	require.Equal(t, 3, cap(stored.RequestOptions[0].Value))
	require.Equal(t, 3, cap(stored.RequestToken))
	require.Equal(t, 3, cap(stored.Controls[0].ReplyToken))
	require.Equal(t, 3, cap(stored.Controls[0].Intent.Action.Numbers))
	work.RequestOptions[0].Value[0] = 'z'
	work.RequestToken[0] = 9
	work.Controls[0].ReplyToken[0] = 9
	work.Controls[0].Intent.Action.Numbers[0] = 9
	require.Equal(t, []byte("abc"), stored.RequestOptions[0].Value)
	require.Equal(t, message.Token{1, 2, 3}, stored.RequestToken)
	require.Equal(t, message.Token{4, 5, 6}, stored.Controls[0].ReplyToken)
	require.Equal(t, []uint32{1, 2, 3}, stored.Controls[0].Intent.Action.Numbers)
	require.NoError(t, q.replace(id, qblockPendingWork{Kind: qblockWorkGET}, true))
	require.Equal(t, uint64(1), q.used)
	q.clearPending(id)
	require.Equal(t, uint64(1), q.used)
	q.release(id)
	require.Zero(t, q.used)
}

func TestQBlockMemoryGETSnapshotAndPendingOptions(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable, cfg.BlockwiseSZX = false, blockwise.SZX16
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), ScheduleMode: qblockScheduleManual}))
	t.Cleanup(session.closeForTest)
	req := newPrivateQBlockClientGET(t, cc, message.Token{1, 2, 3})
	defer cc.ReleaseMessage(req)
	req.SetOptionString(message.ProxyURI, string(bytes.Repeat([]byte{'p'}, 1000)))
	req.SetOptionUint32(message.QBlock2, 8)
	req.SetOptionBytes(message.RequestTag, req.Token())
	budget, err := qblockClientSnapshotCapacity(req.Options(), req.Token(), nil, 10)
	require.NoError(t, err)
	req.Remove(message.QBlock2)
	cc.qblockClient.workQueue.maxBytes = budget
	prepared, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	require.True(t, prepared.Prepared)
	exchange := cc.qblockClient.exchangesByOriginalToken[string(req.Token())]
	slot := cc.qblockClient.workQueue.slots[exchange.workID]
	pendingBytes, err := qblockWorkBytes(*slot.pending)
	require.NoError(t, err)
	snapshotBytes, err := qblockOptionBytes(exchange.requestOpts)
	require.NoError(t, err)
	require.LessOrEqual(t, snapshotBytes+uint64(cap(exchange.originalToken))+pendingBytes, cc.qblockClient.workQueue.used)
	_, err = slot.pending.RequestOptions.GetUint32(message.QBlock2)
	require.NoError(t, err, "queued GET must retain its advertisement")
	proxyCount := 0
	for _, option := range slot.pending.RequestOptions {
		if option.ID == message.ProxyURI {
			proxyCount++
		}
	}
	require.Equal(t, 1, proxyCount)
	cc.qblockClient.abandon(req.Token(), qblock.ErrCanceled)
	require.Zero(t, cc.qblockClient.workQueue.used)
}

func TestQBlockMemoryPreparationRejectsBeforeOwnedBodyRead(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithMaxTransfers(t, session, 1, message.GetToken)
	t.Cleanup(session.closeForTest)
	release, ok := cc.qblockClient.callbackSlots.tryAcquire()
	require.True(t, ok)
	defer release()
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("body"))}
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	req.SetBody(reader)
	_, err := cc.qblockClient.prepareQ1(req, nil)
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	require.Zero(t, reader.readBytes, "rejected preparation must not read owned payload")
	require.Empty(t, session.writesSnapshot())
}

func TestQBlockMemoryOwnedTerminalRespectsDatagramLimit(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConn(t)
	cc.qblockClient.datagramLimit = 68
	req := newPOSTWithBody(t, cc, message.Token{1}, []byte("body"))
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepareQ1(req, nil)
	require.NoError(t, err)
	var token message.Token
	for key, transfer := range cc.qblockClient.transferByToken {
		if transfer.kind == qblock.Q1 {
			token = message.Token([]byte(key))
			break
		}
	}
	require.NotEmpty(t, token)
	terminal := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(terminal)
	terminal.SetCode(codes.Changed)
	terminal.SetType(message.NonConfirmable)
	terminal.SetToken(token)
	terminal.SetBody(bytes.NewReader(make([]byte, 69)))
	require.True(t, cc.qblockClient.handle(terminal))
	require.EqualValues(t, 1, cc.qblockClient.active(), "oversized terminal must not settle upload")
	_ = session
}

func TestQBlockMemoryDatagramSizingDoesNotReadBody(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("body"))}
	req.SetMessageID(1)
	req.SetType(message.NonConfirmable)
	req.SetBody(reader)
	_, err := reader.Seek(2, io.SeekStart)
	require.NoError(t, err)
	size, err := qblockDatagramSize(req)
	require.NoError(t, err)
	require.Greater(t, size, uint64(4))
	require.Zero(t, reader.readBytes, "sizing must not marshal/copy payload")
	position, err := reader.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.EqualValues(t, 2, position)
}

func TestQBlockMemoryHandlerOptionsRejectedBeforeSnapshot(t *testing.T) {
	calls := 0
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("response"))}
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxMetadataBytes: 256}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, reader))
		w.Message().SetOptionBytes(message.LocationQuery, make([]byte, 512))
	})
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	h.ingest(h.q1(t, 2, 0, false, 4, "body"))
	require.Equal(t, 1, calls)
	require.Zero(t, reader.readBytes, "reject options before capturing owned response body")
	require.Empty(t, h.session.writesSnapshot())
	for _, record := range h.cc.qblockClient.server.records {
		require.Nil(t, record.responseOptions)
	}
}

func TestQBlockMemoryCanonicalOptionsUseExactStorageCharge(t *testing.T) {
	opts := message.Options{{ID: message.URIPath, Value: []byte("x")}, {ID: message.QBlock1, Value: []byte{0}}}
	snapshot, err := canonicalServerRequestOptions(opts)
	require.NoError(t, err)
	require.Len(t, snapshot, 1)
	require.Equal(t, len(snapshot), cap(snapshot))
	require.Equal(t, len(snapshot[0].Value), cap(snapshot[0].Value))
	require.Equal(t, uint64(unsafe.Sizeof(message.Option{}))+1, optionsSize(snapshot))
}

func TestQBlockMemoryMissingReportBoundedBeforeDecode(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	msg := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(msg)
	msg.SetCode(codes.RequestEntityIncomplete)
	msg.SetToken(message.Token{1})
	msg.SetContentFormat(message.AppMissingBlocksCBORSeq)
	reader := &qblockCountingReader{Reader: bytes.NewReader(make([]byte, 4096))}
	msg.SetBody(reader)
	_, _, err := q1ControlFromResponseBounded(msg, 128, 16, 2)
	require.Error(t, err)
	require.LessOrEqual(t, reader.readBytes, 17)
}

func TestQBlockMemoryAnnouncedBodyRejectedBeforeFragmentRead(t *testing.T) {
	mc := qblock.DefaultManagerConfig()
	mc.Transfer.MaxBodySize = 16
	h := newServerHarness(t, mc, qblockServerConfig{}, nil)
	req := h.q1(t, 1, 0, true, 32, string(bytes.Repeat([]byte{'u'}, 16)))
	reader := &qblockCountingReader{Reader: bytes.NewReader(bytes.Repeat([]byte{'u'}, 16))}
	req.SetBody(reader)
	h.ingest(req)
	require.Zero(t, reader.readBytes, "reject announced size before sparse validation")
	require.Zero(t, h.cc.qblockClient.active())
}

func TestQBlockMemoryReadBackingHasPredictableLimit(t *testing.T) {
	payload, err := readQBlockBody(bytes.NewReader([]byte("body")), 16)
	require.NoError(t, err)
	require.Equal(t, 17, cap(payload), "one limit+1 backing supports aggregate reservation")
}

type qblockSeekEndFailure struct{ *bytes.Reader }

func (r *qblockSeekEndFailure) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekEnd {
		return 0, errors.New("seek end failed")
	}
	return r.Reader.Seek(offset, whence)
}
func TestQBlockMemorySizingRestoresCursorOnError(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	req.SetMessageID(1)
	req.SetType(message.NonConfirmable)
	reader := &qblockSeekEndFailure{Reader: bytes.NewReader([]byte("body"))}
	_, err := reader.Seek(2, io.SeekStart)
	require.NoError(t, err)
	req.SetBody(reader)
	_, err = qblockDatagramSize(req)
	require.Error(t, err)
	pos, err := reader.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.EqualValues(t, 2, pos)
}
func TestQBlockMemorySizingRejectsInvalidWireHeader(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	_, err := qblockDatagramSize(req)
	require.Error(t, err)
}

func TestQBlockMemoryMIDBudgetBoundsLivePacketOwnership(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cfg.BlockwiseSZX = blockwise.SZX16
	cfg.GetMID = func() int32 { return 1 }
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), MaxMIDEntries: 1}))
	t.Cleanup(session.closeForTest)
	req := newPOSTWithBody(t, cc, message.Token{1}, bytes.Repeat([]byte{'u'}, 32))
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepareQ1(req, nil)
	require.NoError(t, err)
	require.Len(t, session.writesSnapshot(), 1, "live MID cannot be reused for second NON packet")
	require.Zero(t, cc.qblockClient.active(), "failed burst must release ownership")
}

func TestQBlockMemoryAggregateRejectsBeforeSecondBodyRead(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithMaxTransfers(t, session, 2, message.GetToken)
	t.Cleanup(session.closeForTest)
	budget := cc.qblockClient.ownedBudget
	budget.limit = budget.floor + budget.clientCost
	first := newPOSTWithBody(t, cc, message.Token{1}, []byte("body"))
	defer cc.ReleaseMessage(first)
	_, err := cc.qblockClient.prepareQ1(first, nil)
	require.NoError(t, err)
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("other"))}
	second := newPOSTWithBody(t, cc, message.Token{2}, nil)
	defer cc.ReleaseMessage(second)
	second.SetBody(reader)
	_, err = cc.qblockClient.prepareQ1(second, nil)
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	require.Zero(t, reader.readBytes)
	require.Equal(t, budget.floor+budget.clientCost, budget.used)
	cc.qblockClient.abandon(first.Token(), qblock.ErrCanceled)
	require.Equal(t, budget.floor, budget.used)
}

func TestQBlockMemoryAggregateHandlerLeaseOutlivesClose(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(_ *responsewriter.ResponseWriter[*Conn], _ *pool.Message) { close(entered); <-release })
	done := make(chan struct{})
	go func() { h.ingest(h.q1(t, 1, 0, false, 4, "body")); close(done) }()
	<-entered
	budget := h.cc.qblockClient.ownedBudget
	budget.mu.Lock()
	before := budget.used
	budget.mu.Unlock()
	require.Greater(t, before, budget.floor, "running handler must own its envelope")
	h.cc.qblockClient.close()
	budget.mu.Lock()
	after := budget.used
	budget.mu.Unlock()
	require.Equal(t, before, after, "handler copy remains charged after registry close")
	close(release)
	<-done
	budget.mu.Lock()
	after = budget.used
	budget.mu.Unlock()
	require.Equal(t, budget.floor, after)
}

func TestQBlockMemoryAggregateCallbackLeaseOutlivesClose(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithMaxTransfers(t, session, 1, message.GetToken)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	req := newPOSTWithBody(t, cc, message.Token{1}, []byte("body"))
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepareQ1(req, func(error) { close(entered); <-release })
	require.NoError(t, err)
	budget := cc.qblockClient.ownedBudget
	cc.qblockClient.close()
	<-entered
	budget.mu.Lock()
	used := budget.used
	budget.mu.Unlock()
	require.Equal(t, budget.floor+budget.clientCost, used)
	close(release)
	require.Eventually(t, func() bool { budget.mu.Lock(); defer budget.mu.Unlock(); return budget.used == budget.floor }, time.Second, time.Millisecond)
}
func TestQBlockMemoryAggregateOversizedOptionsBeforeBodyRead(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	req := newPOSTWithBody(t, cc, message.Token{1}, nil)
	defer cc.ReleaseMessage(req)
	req.SetOptionBytes(message.URIQuery, make([]byte, 100000))
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("body"))}
	req.SetBody(reader)
	_, err := cc.qblockClient.prepareQ1(req, nil)
	require.Error(t, err)
	require.Zero(t, reader.readBytes)
	require.Equal(t, cc.qblockClient.ownedBudget.floor, cc.qblockClient.ownedBudget.used)
}

func TestQBlockMemoryMIDCollisionPreservesExistingOwner(t *testing.T) {
	cc := newPrivateQBlockClientConn(t)
	owner := &qblockTransfer{mids: map[int32]struct{}{7: {}}}
	cc.qblockClient.mu.Lock()
	cc.qblockClient.transferByMID[7] = owner
	err := cc.qblockClient.reserveMIDLocked(7)
	require.ErrorIs(t, err, qblock.ErrLimitExceeded)
	require.Same(t, owner, cc.qblockClient.transferByMID[7])
	delete(cc.qblockClient.transferByMID, 7)
	require.NoError(t, cc.qblockClient.reserveMIDLocked(7))
	cc.qblockClient.mu.Unlock()
}
func TestQBlockMemoryAggregateInvalidCapAndOverflow(t *testing.T) {
	for _, cap := range []uint64{1, 1000} {
		session := &qblockTestSession{ctx: context.Background()}
		cfg := DefaultConfig
		cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), MaxOwnedBytes: cap}))
		require.Error(t, cc.qblockClient.initErr)
		session.closeForTest()
	}
	_, err := qblockMapAllowance(^uint64(0), 16, 16, 8)
	require.Error(t, err)
}

func TestQBlockMemoryInvalidAggregateConfigRejectsIncoming(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), MaxOwnedBytes: 1}), withQBlockServer(qblockServerConfig{}))
	h := &serverHarness{cc: cc, session: session, nextMID: 1}
	require.NotPanics(t, func() { h.ingest(h.q1(t, 1, 0, false, 4, "body")) })
	require.Zero(t, cc.qblockClient.active())
	session.closeForTest()
}
func TestQBlockMemoryServerClearingMIDsDropsBacking(t *testing.T) {
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, nil)
	record := &qblockServerRecord{mids: map[int32]struct{}{1: {}}}
	h.cc.qblockClient.server.byMID[1] = record
	h.cc.qblockClient.server.clearMIDsLocked(record)
	require.Nil(t, record.mids, "retained suppression must not retain MID map high-water backing")
}
func TestQBlockMemoryPendingGETObeysMIDCapacity(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), MaxMIDEntries: 1}))
	t.Cleanup(session.closeForTest)
	cc.qblockClient.transferByMID[1] = &qblockTransfer{}
	req := newPrivateQBlockClientGET(t, cc, message.Token{1})
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	cc.qblockClient.drivePending(cc.qblockClient.now())
	require.Empty(t, session.writesSnapshot(), "GET cannot bypass MID budget")
	delete(cc.qblockClient.transferByMID, 1)
}

func TestQBlockMemoryHandlerSnapshotFitsPerHandlerEnvelope(t *testing.T) {
	reader := &qblockCountingReader{Reader: bytes.NewReader([]byte("response"))}
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{MaxMetadataBytes: 1 << 20}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, reader))
		w.Message().SetOptionBytes(message.LocationQuery, make([]byte, 100000))
	})
	h.ingest(h.q1(t, 1, 0, false, 4, "body"))
	require.Zero(t, reader.readBytes, "reject before cloning response body")
	for _, record := range h.cc.qblockClient.server.records {
		require.Nil(t, record.responseOptions, "H cannot override per-handler wire-sized envelope")
	}
}

type qblockResetOnRead struct {
	*bytes.Reader
	reset func()
	once  sync.Once
}

func (r *qblockResetOnRead) Read(p []byte) (int, error) { r.once.Do(r.reset); return r.Reader.Read(p) }
func TestQBlockMemoryResetDuringExistingIntakeKeepsAdmissionCharged(t *testing.T) {
	calls := 0
	h := newServerHarness(t, qblock.DefaultManagerConfig(), qblockServerConfig{}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("done"))))
	})
	h.ingest(h.q1(t, 1, 0, true, 32, string(bytes.Repeat([]byte{'u'}, 16))))
	var record *qblockServerRecord
	for _, r := range h.cc.qblockClient.server.records {
		record = r
	}
	require.NotNil(t, record)
	h.cc.qblockClient.mu.Lock()
	require.NoError(t, h.cc.qblockClient.server.bindMIDLocked(record, 123))
	h.cc.qblockClient.mu.Unlock()
	req := h.q1(t, 2, 1, false, 32, string(bytes.Repeat([]byte{'u'}, 16)))
	req.SetBody(&qblockResetOnRead{Reader: bytes.NewReader(bytes.Repeat([]byte{'u'}, 16)), reset: func() { require.True(t, h.cc.qblockClient.server.handleReset(123)) }})
	h.ingest(req)
	for _, r := range h.cc.qblockClient.server.records {
		require.NotNil(t, r.ownedLease, "replacement admission must acquire its lease")
	}
	require.NotPanics(t, func() { h.ingest(h.q1(t, 3, 0, true, 32, string(bytes.Repeat([]byte{'u'}, 16)))) })
	require.Equal(t, 1, calls)
}

func TestQBlockMemoryPendingGETMIDsReleasedOnClose(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cfg.BlockwiseEnable = false
	cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig()}))
	t.Cleanup(session.closeForTest)
	req := newPrivateQBlockClientGET(t, cc, message.Token{7})
	defer cc.ReleaseMessage(req)
	_, err := cc.qblockClient.prepare(req, nil)
	require.NoError(t, err)
	cc.qblockClient.drivePending(cc.qblockClient.now())
	require.Len(t, cc.qblockClient.pendingGETByMID, 1)
	cc.qblockClient.close()
	require.Empty(t, cc.qblockClient.pendingGETByMID)
}
