package qblocktransport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocklink"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

const MTU = 128

var Upload = []byte("0123456789abcdefABCDEFGHIJKLMNOPqrstuvwxyz0123456789abcdefghijklMNOPQRSTUVWXYZabcd")
var Response = []byte("response-block00response-block01response-block02response-block03response-block04response-block05")

// Configs use three or more sets at SZX16 with protocol-valid receive timing.
func Configs() (qblock.ClientConfig, qblock.ServerConfig) {
	c := qblock.DefaultClientConfig()
	c.Mode = qblock.Require
	c.Manager.Transfer.MaxPayloads = 2
	c.Manager.Transfer.NonTimeout = 100 * time.Millisecond
	c.Manager.Transfer.NonReceiveTimeout = 1150 * time.Millisecond
	c.Manager.Transfer.Lifetime = 30 * time.Second
	c.ProbingRate = 65536
	c.NonProbingWait = 10 * time.Millisecond
	s := qblock.DefaultServerConfig()
	s.Manager = c.Manager
	s.ProbingRate = c.ProbingRate
	s.NonProbingWait = c.NonProbingWait
	s.Retention = c.Manager.Transfer.Lifetime
	return c, s
}

func Rules(code codes.Code) []qblocklink.Rule {
	r := []qblocklink.Rule{
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 1, Action: qblocklink.Drop},
		{Direction: qblocklink.ServerToClient, Kind: qblocklink.Q2, Occurrence: 2, Action: qblocklink.Duplicate},
	}
	if code != codes.GET {
		r = append(r,
			qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 2, Action: qblocklink.Drop},
			qblocklink.Rule{Direction: qblocklink.ClientToServer, Kind: qblocklink.Q1, Occurrence: 3, Action: qblocklink.Duplicate})
	}
	return r
}

type Delivery struct {
	Code codes.Code
	Path string
	Body []byte
	Err  error
}

// CheckDeliveries runs after connection/server shutdown when registered before
// their cleanups, so delayed duplicates cannot escape the exactly-once check.
func CheckDeliveries(t *testing.T, deliveries <-chan Delivery) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case extra := <-deliveries:
			t.Errorf("unexpected handler delivery after shutdown: %+v", extra)
		default:
		}
	})
}

func Handler(deliveries chan<- Delivery) func(*responsewriter.ResponseWriter[*client.Conn], *pool.Message) {
	return func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		path, err := r.Path()
		body, bodyErr := r.ReadBody()
		if err == nil {
			err = bodyErr
		}
		status := codes.Content
		payload := Response
		if path == "/probe" {
			payload = []byte("probe")
		} else if r.Code() != codes.GET {
			status = codes.Changed
		}
		if responseErr := w.SetResponse(status, message.TextPlain, bytes.NewReader(payload)); err == nil {
			err = responseErr
		}
		deliveries <- Delivery{Code: r.Code(), Path: path, Body: body, Err: err}
	}
}

// Run uses only the public client operations. A suppressed repair, duplicate
// handler delivery, corrupted body, or missing capability check fails here.
func Run(t *testing.T, ctx context.Context, cc *client.Conn, controller *Controller, code codes.Code, deliveries <-chan Delivery) {
	t.Helper()
	_, err := cc.Get(ctx, "/unknown")
	require.ErrorIs(t, err, qblock.ErrCapabilityUnknown)
	supported, err := cc.ProbeQBlock(ctx, "/probe")
	require.NoError(t, err)
	require.True(t, supported)
	probe := receiveDelivery(t, ctx, deliveries)
	require.NoError(t, probe.Err)
	require.Equal(t, "/probe", probe.Path)
	require.Empty(t, probe.Body)
	require.NoError(t, controller.Arm(Rules(code)))
	var resp *pool.Message
	switch code {
	case codes.GET:
		resp, err = cc.Get(ctx, "/body")
	case codes.POST:
		resp, err = cc.Post(ctx, "/body", message.TextPlain, bytes.NewReader(Upload))
	case codes.PUT:
		resp, err = cc.Put(ctx, "/body", message.TextPlain, bytes.NewReader(Upload))
	}
	require.NoError(t, err)
	require.NotNil(t, resp)
	defer cc.ReleaseMessage(resp)
	body, err := resp.ReadBody()
	require.NoError(t, err)
	require.Equal(t, Response, body)
	status := codes.Content
	if code != codes.GET {
		status = codes.Changed
	}
	require.Equal(t, status, resp.Code())
	delivery := receiveDelivery(t, ctx, deliveries)
	require.NoError(t, delivery.Err)
	require.Equal(t, code, delivery.Code)
	require.Equal(t, "/body", delivery.Path)
	if code == codes.GET {
		require.Empty(t, delivery.Body)
	} else {
		require.Equal(t, Upload, delivery.Body)
	}
	select {
	case extra := <-deliveries:
		t.Fatalf("duplicate handler delivery: %+v", extra)
	default:
	}
}
func receiveDelivery(t *testing.T, ctx context.Context, deliveries <-chan Delivery) Delivery {
	t.Helper()
	select {
	case delivery := <-deliveries:
		return delivery
	case <-ctx.Done():
		t.Fatal("handler delivery deadline: ", ctx.Err())
		return Delivery{}
	}
}

func AssertTrace(t *testing.T, snapshot Snapshot, code codes.Code) {
	t.Helper()
	AssertForwarded(t, snapshot)
	require.NotEmpty(t, snapshot.Probe)
	probe := pool.NewMessage(context.Background())
	_, err := probe.UnmarshalWithDecoder(qblock.Decoder{}, snapshot.Probe[0].Wire)
	require.NoError(t, err)
	require.Equal(t, message.Confirmable, probe.Type())
	require.Equal(t, codes.GET, probe.Code())
	path, err := probe.Path()
	require.NoError(t, err)
	require.Equal(t, "/probe", path)
	require.True(t, probe.HasOption(message.QBlock2))
	for _, rule := range Rules(code) {
		matched := 0
		for _, event := range snapshot.Faults {
			if event.Direction == rule.Direction && event.Kind == rule.Kind && event.Occurrence == rule.Occurrence {
				require.Equal(t, rule.Action, event.Action)
				matched++
			}
		}
		require.Equal(t, 1, matched, "fault selector %+v must execute", rule)
	}
	q1Numbers, q2Numbers := make(map[uint32]int), make(map[uint32]int)
	missing, repair := false, false
	for _, event := range snapshot.Faults {
		require.NotEqual(t, qblocklink.Malformed, event.Kind)
		m := pool.NewMessage(context.Background())
		_, err := m.UnmarshalWithDecoder(qblock.Decoder{}, event.Wire)
		require.NoError(t, err)
		if event.Kind == qblocklink.Missing {
			missing = true
		}
		if event.Direction == qblocklink.ClientToServer && event.Kind == qblocklink.Q2 && event.Occurrence > 1 {
			repair = true
		}
		var option message.OptionID
		var numbers map[uint32]int
		if event.Direction == qblocklink.ClientToServer && event.Kind == qblocklink.Q1 {
			option, numbers = message.QBlock1, q1Numbers
		}
		if event.Direction == qblocklink.ServerToClient && event.Kind == qblocklink.Q2 {
			option, numbers = message.QBlock2, q2Numbers
		}
		if numbers == nil {
			continue
		}
		value, err := m.GetOptionUint32(option)
		require.NoError(t, err)
		require.Zero(t, value&7, "configured SZX16 must be sent")
		require.Equal(t, message.NonConfirmable, m.Type())
		numbers[value>>4]++
	}
	require.GreaterOrEqual(t, len(q2Numbers), 6, "response must cross three payload sets")
	require.GreaterOrEqual(t, q2Numbers[0], 2, "dropped Q2 zero must be repaired")
	require.True(t, repair, "client must send a Q2 repair request")
	if code != codes.GET {
		require.GreaterOrEqual(t, len(q1Numbers), 5, "upload must cross three payload sets")
		require.GreaterOrEqual(t, q1Numbers[1], 2, "dropped Q1 one must be repaired")
		require.True(t, missing, "server must report missing Q1")
	}
}

// AssertForwarded compares the input decision with successful output crossings.
// Queued plaintext read duplicates are counted only when the consumer reads them.
func AssertForwarded(t *testing.T, snapshot Snapshot) {
	t.Helper()
	counts := make(map[forwardKey]int)
	for _, forwarded := range snapshot.Forwarded {
		counts[forwardKey{forwarded.Phase, forwarded.Direction, forwarded.ID}] += forwarded.Count
	}
	for _, phase := range []struct {
		name   string
		events []qblocklink.Event
	}{{"probe", snapshot.Probe}, {"faults", snapshot.Faults}} {
		for _, event := range phase.events {
			want := 1
			switch event.Action {
			case qblocklink.Drop:
				want = 0
			case qblocklink.Duplicate:
				want = 2
			}
			key := forwardKey{phase.name, event.Direction, event.ID}
			require.Equal(t, want, counts[key], "actual forwarding for phase %s packet %d direction %d action %s", phase.name, event.ID, event.Direction, event.Action)
			delete(counts, key)
		}
	}
	require.Empty(t, counts, "successful forwarding must reference an accepted input")
}

// SaveTrace registers before socket cleanups, so final evidence follows shutdown
// even when a test fails. Set QBLOCK_TRACE_DIR to retain artifacts outside TempDir.
func SaveTrace(t *testing.T, transport string, controller *Controller, code codes.Code) {
	t.Helper()
	dir := t.TempDir()
	if configured := os.Getenv("QBLOCK_TRACE_DIR"); configured != "" {
		require.True(t, filepath.IsAbs(configured), "QBLOCK_TRACE_DIR must be absolute")
		dir = configured
	}
	t.Cleanup(func() {
		c, s := Configs()
		artifact := struct {
			Test, Transport, Method, Injection, Authentication string
			MTU, SZX                                           int
			Client                                             qblock.ClientConfig
			Server                                             qblock.ServerConfig
			Rules                                              []qblocklink.Rule
			Trace                                              Snapshot
		}{t.Name(), transport, code.String(), "CoAP datagrams", "none", MTU, 0, c, s, Rules(code), controller.Snapshot()}
		if transport == "dtls" {
			artifact.Injection = "authenticated decrypted Read and plaintext Write on accepted PSK-DTLS connection"
			artifact.Authentication = "PSK TLS_PSK_WITH_AES_128_CCM_8"
		}
		wire, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if t.Failed() {
			t.Logf("socket trace JSON: %s", wire)
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Error(err)
			return
		}
		name := strings.ReplaceAll(t.Name(), "/", "-") + ".json"
		if err = os.WriteFile(filepath.Join(dir, name), wire, 0600); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			t.Logf("socket trace retained: %s", filepath.Join(dir, name))
		}
	})
	t.Cleanup(func() {
		if !t.Failed() {
			AssertTrace(t, controller.Snapshot(), code)
		}
	})
}
