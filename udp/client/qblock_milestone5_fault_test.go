package client

import (
	"bytes"
	"context"
	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// Literal occurrence rules lose blocks in multiple sets, or deliver block1
// before block0. Both roles must complete without another application call.
func TestQBlockMilestone5CrossSetAndReorder(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{{"POST", codes.POST}, {"PUT", codes.PUT}} {
		for _, reorder := range []bool{false, true} {
			name := "cross_set_loss"
			if reorder {
				name = "held_reorder"
			}
			t.Run(method.name+"/"+name, func(t *testing.T) {
				rules := []qblocklink.Rule{{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop}}
				if reorder {
					rules = append(rules, qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 1, Action: qblocklink.Hold})
				} else {
					for _, n := range []uint64{2, 4} {
						rules = append(rules, qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: n, Action: qblocklink.Drop})
					}
				}
				l, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 2048, MaxBytes: 1 << 20})
				require.NoError(t, e)
				runQBlockPacingPairedScenario(t, method.code, &qblockPacingConfig{ProbingRate: 1024, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute, l, 80, reorder)
				trace := l.Trace()
				if reorder {
					require.Equal(t, qblocklink.Hold, trace[0].Action)
					require.Equal(t, qblocklink.Pass, trace[1].Action)
					require.Equal(t, qblocklink.Released, trace[2].Action)
					require.Equal(t, trace[0].ID, trace[2].ID)
					require.Equal(t, trace[0].Wire, trace[2].Wire)
				} else {
					drops := 0
					for _, event := range trace {
						if event.Direction == qblocklink.ClientToServer && event.Kind == qblocklink.Q1 && event.Action == qblocklink.Drop {
							drops++
							require.Contains(t, []uint64{2, 4}, event.Occurrence)
						}
					}
					require.Equal(t, 2, drops)
				}
			})
		}
	}
}

// Total initial-data loss cannot invoke a peer handler; the sender expires
// once and releases ownership instead of replaying the application operation.
func TestQBlockMilestone5AllInitialLoss(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{{"POST", codes.POST}, {"PUT", codes.PUT}} {
		t.Run(method.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			clock := newFakeQBlockClock(now)
			cfg := qblock.DefaultManagerConfig()
			cfg.Transfer.MaxPayloads = 2
			session := &qblockTestSession{ctx: context.Background()}
			cc := newQBlockClockTestConnWithSession(t, session, qblockClientConfig{Manager: cfg, Clock: clock, ScheduleMode: qblockScheduleManual})
			failures := make(chan error, 2)
			req := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(req)
			req.SetCode(method.code)
			req.SetToken(message.Token{0xa9})
			require.NoError(t, req.SetPath("/lost"))
			req.SetBody(bytes.NewReader(bytes.Repeat([]byte{'u'}, 80)))
			prepared, e := cc.qblockClient.prepareQ1(req, func(e error) { failures <- e })
			require.NoError(t, e)
			require.True(t, prepared.Prepared)
			clock.Advance(cfg.Transfer.NonTimeout)
			cc.qblockClient.Tick(clock.Now())
			clock.Advance(cfg.Transfer.NonTimeout)
			cc.qblockClient.Tick(clock.Now())
			writes := session.writesSnapshot()
			require.Len(t, writes, 5)
			rules := make([]qblocklink.Rule, 5)
			for i := range rules {
				rules[i] = qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: uint64(i + 1), Action: qblocklink.Drop}
			}
			link, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 100, MaxBytes: 4096})
			require.NoError(t, e)
			for i, w := range writes {
				packets := processQBlockRelayWire(t, link, qblocklink.ClientToServer, pairedQBlockWire{typ: w.typ, code: w.code, token: w.token, mid: w.mid, options: w.options, payload: w.payload})
				require.Empty(t, packets)
				b, e := qblock.DecodeBlock(w.block)
				require.NoError(t, e)
				require.Equal(t, uint32(i), b.Number)
				require.Equal(t, blockwise.SZX16, b.SZX)
			}
			require.Len(t, link.Trace(), 5)
			clock.Advance(cfg.Transfer.Lifetime)
			cc.qblockClient.Tick(clock.Now())
			require.Error(t, requireQBlockFailure(t, failures))
			requireQBlockClientEmpty(t, cc)
			cc.qblockClient.Tick(clock.Now())
			require.Len(t, session.writesSnapshot(), 5)
			select {
			case e := <-failures:
				t.Fatalf("duplicate failure %v", e)
			default:
			}
		})
	}
}

// One-way response loss permits one upload delivery but never an application
// replay; terminal uncertainty expires and retained server state is bounded.
func TestQBlockMilestone5AsymmetricResponseLoss(t *testing.T) {
	cfg := qblock.DefaultManagerConfig()
	cfg.Transfer.MaxPayloads = 2
	calls := 0
	h := newServerHarness(t, cfg, qblockServerConfig{Retention: 2 * cfg.Transfer.Lifetime}, func(w *responsewriter.ResponseWriter[*Conn], _ *pool.Message) {
		calls++
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	})
	for i := uint32(0); i < 3; i++ {
		h.ingest(h.q1(t, byte(20+i), i, i < 2, 48, string(bytes.Repeat([]byte{'u'}, 16))))
	}
	require.Equal(t, 1, calls)
	writes := h.session.writesSnapshot()
	require.NotEmpty(t, writes)
	rules := make([]qblocklink.Rule, 3)
	for i := range rules {
		rules[i] = qblocklink.Rule{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: uint64(i + 1), Action: qblocklink.Drop}
	}
	link, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 100, MaxBytes: 4096})
	require.NoError(t, e)
	for _, w := range writes {
		if !w.options.HasOption(message.QBlock2) {
			continue
		}
		require.Empty(t, processQBlockRelayWire(t, link, qblocklink.ServerToClient, pairedQBlockWire{typ: w.typ, code: w.code, token: w.token, mid: w.mid, options: w.options, payload: w.payload}))
	}
	require.NotEmpty(t, link.Trace())
	h.advance(cfg.Transfer.Lifetime)
	require.Zero(t, h.snapshot().active)
	h.ingest(h.q1(t, 25, 2, false, 48, string(bytes.Repeat([]byte{'u'}, 16))))
	require.Equal(t, 1, calls, "lost response must not rerun completed upload")
	h.advance(cfg.Transfer.Lifetime)
	require.Equal(t, serverSnapshot{}, h.snapshot())
}

// Raw encoded malformed controls must release exactly their live upload,
// even when the production raw-option decoder preserves an illegal Q value.
func TestQBlockMilestone5MalformedControlWire(t *testing.T) {
	for _, badCBOR := range []bool{false, true} {
		name := "illegal_q_length"
		if badCBOR {
			name = "invalid_cbor"
		}
		t.Run(name, func(t *testing.T) {
			cc, session, failures := startFailingQ1POST(t)
			source := q1Missing(t, cc, session.writes[1].token, []uint32{1})
			defer cc.ReleaseMessage(source)
			source.SetType(message.NonConfirmable)
			source.SetMessageID(90)
			if badCBOR {
				source.SetBody(bytes.NewReader([]byte{255}))
			} else {
				source.SetOptionBytes(message.QBlock1, []byte{0, 0, 0, 0})
			}
			raw, e := source.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, e)
			target := cc.AcquireMessage(context.Background())
			_, e = target.UnmarshalWithDecoder(qblock.Decoder{}, raw)
			require.NoError(t, e)
			handled := cc.qblockClient.handle(target)
			cc.ReleaseMessage(target)
			require.True(t, handled)
			require.Error(t, requireQBlockFailure(t, failures))
			requireQBlockClientEmpty(t, cc)
			select {
			case e := <-failures:
				t.Fatalf("duplicate failure %v", e)
			default:
			}
		})
	}
}

// RFC default geometry spans sets0–9 and10–19. Repair block1/9 in the
// preceding set and block10 in the next while retaining distinct body bytes.
func TestQBlockMilestone5DefaultSetGeometry(t *testing.T) {
	rules := []qblocklink.Rule{{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop}}
	for _, n := range []uint64{2, 10, 11} {
		rules = append(rules, qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: n, Action: qblocklink.Drop})
	}
	link, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 4096, MaxBytes: 1 << 20})
	require.NoError(t, e)
	runQBlockPacingPairedGeometry(t, codes.PUT, &qblockPacingConfig{ProbingRate: 65536, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute, link, 13*16, false, 10)
	var lost []uint32
	for _, event := range link.Trace() {
		if event.Direction != qblocklink.ClientToServer || event.Kind != qblocklink.Q1 || event.Action != qblocklink.Drop {
			continue
		}
		m := pool.NewMessage(context.Background())
		_, e = m.UnmarshalWithDecoder(qblock.Decoder{}, event.Wire)
		require.NoError(t, e)
		value, e := m.GetOptionUint32(message.QBlock1)
		require.NoError(t, e)
		b, e := qblock.DecodeBlock(value)
		require.NoError(t, e)
		lost = append(lost, b.Number)
	}
	require.Equal(t, []uint32{1, 9, 10}, lost)
}

// The raw decode boundary preserves a changed ETag and the receiver must fail
// the original exchange instead of appending bytes from another representation.
func TestQBlockMilestone5RepresentationWire(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cc := newPrivateQBlockClientConnWithToken(t, session, message.GetToken)
	req := newPrivateQBlockClientGET(t, cc, message.Token{0x93})
	defer cc.ReleaseMessage(req)
	failures := make(chan error, 2)
	prepared, e := cc.qblockClient.prepare(req, func(e error) { failures <- e })
	require.NoError(t, e)
	require.True(t, prepared.Prepared)
	for i := uint32(0); i < 2; i++ {
		source := newQBlockClientFragment(t, cc, req.Token(), i, i == 0, 32)
		source.SetType(message.NonConfirmable)
		source.SetMessageID(int32(200 + i))
		if i == 1 {
			require.NoError(t, source.SetETag([]byte("etag-b")))
		}
		raw, e := source.MarshalWithEncoder(coder.DefaultCoder)
		cc.ReleaseMessage(source)
		require.NoError(t, e)
		target := cc.AcquireMessage(context.Background())
		_, e = target.UnmarshalWithDecoder(qblock.Decoder{}, raw)
		require.NoError(t, e)
		handled := cc.qblockClient.handle(target)
		cc.ReleaseMessage(target)
		require.True(t, handled)
	}
	require.ErrorContains(t, requireQBlockFailure(t, failures), "metadata changed")
	requireQBlockClientEmpty(t, cc)
}

// Losing the first repair response as well as the original block requires
// another request; the completed upload still invokes its handler once.
func TestQBlockMilestone5LostRepairResponse(t *testing.T) {
	rules := []qblocklink.Rule{
		{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Drop},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 4, Action: qblocklink.Drop},
	}
	link, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 4096, MaxBytes: 1 << 20})
	require.NoError(t, e)
	runQBlockPacingPairedScenario(t, codes.POST, &qblockPacingConfig{ProbingRate: 1024, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute, link, 48, false)
	var lost []uint32
	var requests int
	for _, event := range link.Trace() {
		if event.Direction == qblocklink.ClientToServer && event.Kind == qblocklink.Q2 {
			requests++
		}
		if event.Direction != qblocklink.ServerToClient || event.Kind != qblocklink.Q2 || event.Action != qblocklink.Drop {
			continue
		}
		m := pool.NewMessage(context.Background())
		_, e = m.UnmarshalWithDecoder(qblock.Decoder{}, event.Wire)
		require.NoError(t, e)
		value, e := m.GetOptionUint32(message.QBlock2)
		require.NoError(t, e)
		lost = append(lost, value>>4)
	}
	require.Equal(t, []uint32{0, 0}, lost)
	require.GreaterOrEqual(t, requests, 2)
}
