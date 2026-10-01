package client

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func processQBlockRelayWire(t *testing.T, l *qblocklink.Link, d qblocklink.Direction, w pairedQBlockWire) []qblocklink.Packet {
	t.Helper()
	m := message.Message{Type: w.typ, Code: w.code, Token: w.token, MessageID: w.mid, Options: w.options, Payload: w.payload}
	n, e := coder.DefaultCoder.Size(m)
	require.NoError(t, e)
	wire := make([]byte, n)
	_, e = coder.DefaultCoder.Encode(m, wire)
	require.NoError(t, e)
	packets, e := l.Process(d, wire)
	require.NoError(t, e)
	return packets
}
func deliverQBlockRelayPacket(t *testing.T, target *Conn, p qblocklink.Packet) {
	t.Helper()
	m := target.AcquireMessage(context.Background())
	_, e := m.UnmarshalWithDecoder(qblock.Decoder{}, p.Wire)
	require.NoError(t, e)
	if m.Code() < 32 {
		target.ProcessReceivedMessage(m)
		return
	}
	handled := target.qblockClient.handle(m)
	target.ReleaseMessage(m)
	require.True(t, handled)
}

// Bypassing a scripted drop/duplicate or corrupting a repair must fail.
func TestQBlockRelayCombinedFaults(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{{"POST", codes.POST}, {"PUT", codes.PUT}} {
		t.Run(method.name, func(t *testing.T) {
			l, e := qblocklink.New([]qblocklink.Rule{
				{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Drop},
				{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop},
				{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 2, Action: qblocklink.Duplicate},
			}, qblocklink.Limits{MaxEvents: 2048, MaxBytes: 1 << 20})
			require.NoError(t, e)
			traceDir := t.TempDir()
			if configured := os.Getenv("QBLOCK_TRACE_DIR"); configured != "" {
				traceDir = configured
			}
			t.Cleanup(func() {
				artifact := struct {
					Method      string
					SZX         int
					MaxPayloads int
					Lifetime    string
					Events      []qblocklink.Event
				}{method.name, 0, 2, "2m", l.Trace()}
				b, e := json.MarshalIndent(artifact, "", "  ")
				if e != nil {
					t.Error(e)
					return
				}
				path := filepath.Join(traceDir, method.name+"-trace.json")
				if e = os.MkdirAll(traceDir, 0700); e != nil {
					t.Error(e)
					return
				}
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Error(e)
				}
			})
			runQBlockPacingPairedRepairWithRelay(t, method.code, &qblockPacingConfig{ProbingRate: 1024, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute, l)
			trace := l.Trace()
			drops, dups, q1s, q2s := 0, 0, 0, 0
			for _, e := range trace {
				require.NotEqual(t, qblocklink.Malformed, e.Kind)
				if e.Direction == qblocklink.ClientToServer && e.Kind == qblocklink.Q1 {
					q1s++
					if e.Action == qblocklink.Drop {
						drops++
						require.Equal(t, uint64(2), e.Occurrence)
						require.Equal(t, byte(method.code), e.Wire[1])
					}
				}
				if e.Direction == qblocklink.ServerToClient && e.Kind == qblocklink.Q2 {
					q2s++
					if e.Action == qblocklink.Drop {
						drops++
						require.Equal(t, uint64(1), e.Occurrence)
					}
					if e.Action == qblocklink.Duplicate {
						dups++
						require.Equal(t, uint64(2), e.Occurrence)
					}
				}
			}
			require.Equal(t, 2, drops)
			require.Equal(t, 1, dups)
			require.GreaterOrEqual(t, q1s, 4)
			require.GreaterOrEqual(t, q2s, 4)
			// The trace keeps the original transmitted upload block even after loss.
			require.True(t, bytes.Contains(trace[1].Wire, bytes.Repeat([]byte{'u'}, 16)))
		})
	}
}

// Removing recovery-control loss or suppressing its retry must fail.
func TestQBlockRelayRecoveryControlLoss(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{{"POST", codes.POST}, {"PUT", codes.PUT}} {
		for _, control := range []struct {
			name      string
			direction qblocklink.Direction
			kind      qblocklink.Kind
		}{
			{"missing_report", qblocklink.ServerToClient, qblocklink.Missing},
			{"q2_repair", qblocklink.ClientToServer, qblocklink.Q2},
		} {
			t.Run(method.name+"/"+control.name, func(t *testing.T) {
				rules := []qblocklink.Rule{
					{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Drop},
					{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop},
					{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 2, Action: qblocklink.Duplicate},
					{Direction: control.direction, Kind: control.kind, Occurrence: 1, Action: qblocklink.Drop},
				}
				l, e := qblocklink.New(rules, qblocklink.Limits{MaxEvents: 2048, MaxBytes: 1 << 20})
				require.NoError(t, e)
				traceDir := t.TempDir()
				if configured := os.Getenv("QBLOCK_TRACE_DIR"); configured != "" {
					traceDir = configured
				}
				t.Cleanup(func() {
					artifact := struct {
						Method  string
						Control string
						Rules   []qblocklink.Rule
						Events  []qblocklink.Event
					}{method.name, control.name, rules, l.Trace()}
					b, e := json.MarshalIndent(artifact, "", "  ")
					if e != nil {
						t.Error(e)
						return
					}
					if e = os.MkdirAll(traceDir, 0700); e != nil {
						t.Error(e)
						return
					}
					if e = os.WriteFile(filepath.Join(traceDir, method.name+"-"+control.name+"-trace.json"), b, 0600); e != nil {
						t.Error(e)
					}
				})
				runQBlockPacingPairedRepairWithRelay(t, method.code, &qblockPacingConfig{ProbingRate: 1024, NonProbingWait: time.Second, MaxIntentBytes: 1 << 20}, 2*time.Minute, l)
				dropped, retried := false, false
				for _, event := range l.Trace() {
					if event.Direction != control.direction || event.Kind != control.kind {
						continue
					}
					if event.Occurrence == 1 {
						require.Equal(t, qblocklink.Drop, event.Action)
						dropped = true
					}
					if event.Occurrence > 1 {
						require.True(t, dropped)
						require.Equal(t, qblocklink.Pass, event.Action)
						retried = true
					}
				}
				require.True(t, dropped, "selected recovery control must be lost")
				require.True(t, retried, "recovery control must be retried after loss")
			})
		}
	}
}
