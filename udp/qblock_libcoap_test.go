package udp_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

const (
	qLibcoapPSK      = "qblock-test-key"
	qLibcoapEnabled  = "enabled"
	qLibcoapDisabled = "disabled"
)

// boundedCommandLog permits simultaneous subprocess writes and readiness
// diagnostics without retaining unbounded packet-level debug logs.
type boundedCommandLog struct {
	mu   sync.Mutex
	data []byte
}

func (l *boundedCommandLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const limit = 64 * 1024
	if len(p) >= limit {
		l.data = append(l.data[:0], p[len(p)-limit:]...)
	} else {
		if excess := len(l.data) + len(p) - limit; excess > 0 {
			copy(l.data, l.data[excess:])
			l.data = l.data[:len(l.data)-excess]
		}
		l.data = append(l.data, p...)
	}
	return len(p), nil
}

func (l *boundedCommandLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.data)
}

func qLibcoapBinary(t *testing.T, name string) string {
	t.Helper()
	candidates := []string{name, name + "-openssl"}
	if name == "coap-server" {
		candidates = append(candidates, name+"-notls")
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	if os.Getenv("LIBCOAP_QBLOCK_REQUIRED") == "1" {
		t.Fatalf("required libcoap executable %s is unavailable", name)
	}
	t.Skipf("%s unavailable; use pinned Q-enabled libcoap (see .github/workflows/test-qblock.yml)", name)
	return ""
}

func qLibcoapMode(t *testing.T) string {
	t.Helper()
	mode := os.Getenv("LIBCOAP_QBLOCK_MODE")
	if mode == "" {
		mode = qLibcoapEnabled
	}
	require.Contains(t, []string{qLibcoapEnabled, qLibcoapDisabled}, mode)
	return mode
}

func qLibcoapPSKCallback([]byte) ([]byte, error) {
	return []byte(qLibcoapPSK), nil
}

type qWireCounts struct {
	q1     atomic.Int32
	q2     atomic.Int32
	legacy atomic.Int32
}

func (c *qWireCounts) monitor(_ *client.Conn, msg *pool.Message) (bool, error) {
	// Bad Option may echo the rejected Q option; it is not Q transfer traffic.
	if msg.Code() >= codes.BadRequest {
		return false, nil
	}
	if msg.HasOption(message.QBlock1) {
		c.q1.Add(1)
	}
	if msg.HasOption(message.QBlock2) {
		c.q2.Add(1)
	}
	if msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		c.legacy.Add(1)
	}
	return false, nil
}

func TestQBlockWireCountsExcludeRejectedOptions(t *testing.T) {
	counts := new(qWireCounts)
	msg := pool.NewMessage(context.Background())
	msg.SetCode(codes.BadOption)
	msg.SetOptionUint32(message.QBlock2, 0)
	_, err := counts.monitor(nil, msg)
	require.NoError(t, err)
	require.Zero(t, counts.q2.Load())

	msg.SetCode(codes.Content)
	_, err = counts.monitor(nil, msg)
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.q2.Load())

	msg.Remove(message.QBlock2)
	msg.SetCode(codes.PUT)
	msg.SetOptionUint32(message.QBlock1, 0)
	_, err = counts.monitor(nil, msg)
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.q1.Load())

	msg.Remove(message.QBlock1)
	msg.SetCode(codes.Content)
	msg.SetOptionUint32(message.Block2, 0)
	_, err = counts.monitor(nil, msg)
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.legacy.Load())
}

func qPayload() []byte {
	payload := make([]byte, 4099)
	for i := range payload {
		payload[i] = byte((i*37 + i/251) & 0xff)
	}
	return payload
}

func qDial(t *testing.T, addr string, secure bool, szx blockwise.SZX, mode options.QBlockwiseMode, counts *qWireCounts, parameters ...options.QBlockwiseConfig) *client.Conn {
	t.Helper()
	var cc *client.Conn
	var err error
	var config options.QBlockwiseConfig
	if len(parameters) > 0 {
		config = parameters[0]
	}
	if secure {
		cc, err = dtls.Dial(addr, dtls.NewDTLSClientOptions(
			piondtls.WithPSK(qLibcoapPSKCallback),
			piondtls.WithPSKIdentityHint([]byte("qblock-test")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256)),
			options.WithBlockwise(true, szx, 30*time.Second),
			options.WithQBlockwise(mode),
			options.WithQBlockwiseConfig(config),
			options.WithRequestMonitor(counts.monitor),
		)
	} else {
		cc, err = udp.Dial(addr,
			options.WithBlockwise(true, szx, 30*time.Second),
			options.WithQBlockwise(mode), options.WithQBlockwiseConfig(config), options.WithRequestMonitor(counts.monitor),
		)
	}
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cc.Close()) })
	return cc
}

func startQLibcoapServer(t *testing.T, binary string, enabled bool, sizes ...blockwise.SZX) string {
	t.Helper()
	port := reserveQLibcoapPort(t)
	mode := "3" // USE_LIBCOAP | SINGLE_BODY
	if enabled {
		mode = "7" // plus TRY_Q_BLOCK
	}
	ctx, cancel := context.WithCancel(context.Background())
	szx := blockwise.SZX1024
	if len(sizes) > 0 {
		szx = sizes[0]
	}
	cmd := exec.CommandContext(ctx, binary, "-A", "127.0.0.1", "-p", strconv.Itoa(port),
		"-d", "100", "-e", "-k", qLibcoapPSK, "-L", mode, "-v", "7",
		"-b", strconv.FormatInt(szx.Size(), 10))
	log := new(boundedCommandLog)
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var exited bool
	t.Cleanup(func() {
		cancel()
		stopQLibcoapServer(ctx, t, done, exited, log)
	})
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitQLibcoapServer(t, addr, done, &exited, log)
	return addr
}

func reserveQLibcoapPort(t *testing.T) int {
	t.Helper()
	// libcoap listens on port for UDP and port+1 for DTLS.
	var port int
	for attempt := 0; attempt < 20; attempt++ {
		l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		port = l.LocalAddr().(*net.UDPAddr).Port
		if port == 65535 {
			require.NoError(t, l.Close())
			continue
		}
		next, nextErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
		require.NoError(t, l.Close())
		if nextErr == nil {
			require.NoError(t, next.Close())
			break
		}
		port = 0
	}
	require.NotZero(t, port, "could not reserve consecutive libcoap ports")
	return port
}

func stopQLibcoapServer(ctx context.Context, t *testing.T, done <-chan error, exited bool, log *boundedCommandLog) {
	if !exited {
		select {
		case err := <-done:
			if err != nil && ctx.Err() == nil {
				t.Errorf("libcoap server exited unexpectedly: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("libcoap server failed to stop after cancellation")
		}
	}
	if t.Failed() {
		t.Logf("libcoap server log:\n%s", log.String())
	}
}

func waitQLibcoapServer(t *testing.T, addr string, done <-chan error, exited *bool, log *boundedCommandLog) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			*exited = true
			t.Fatalf("libcoap server exited before readiness: %v\n%s", err, log.String())
		default:
		}
		// An ICMP port-unreachable before libcoap binds closes a Go connection;
		// each readiness attempt therefore needs a fresh connection.
		cc, err := udp.Dial(addr, options.WithErrors(func(err error) {
			_, _ = fmt.Fprintf(log, "readiness probe: %v\n", err)
		}))
		require.NoError(t, err)
		probeCtx, cancelProbe := context.WithTimeout(context.Background(), 100*time.Millisecond)
		resp, probeErr := cc.Get(probeCtx, "/.well-known/core")
		cancelProbe()
		if probeErr == nil {
			cc.ReleaseMessage(resp)
			require.NoError(t, cc.Close())
			return
		}
		require.NoError(t, cc.Close())
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("libcoap server did not become ready:\n%s", log.String())
}

func qSecureAddr(t *testing.T, addr string, secure bool) string {
	t.Helper()
	if !secure {
		return addr
	}
	host, portText, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return net.JoinHostPort(host, strconv.Itoa(port+1))
}

func qDo(t *testing.T, cc *client.Conn, method codes.Code, path string, payload []byte, typ message.Type, opts ...message.Option) *pool.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var req *pool.Message
	var err error
	if method == codes.GET {
		req, err = cc.NewGetRequest(ctx, path, opts...)
	} else {
		req, err = cc.NewPutRequest(ctx, path, message.AppOctets, bytes.NewReader(payload), opts...)
	}
	require.NoError(t, err)
	defer cc.ReleaseMessage(req)
	req.SetType(typ)
	resp, err := cc.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cc.ReleaseMessage(resp) })
	return resp
}

func TestQBlockLibcoapGoClient(t *testing.T) {
	binary := qLibcoapBinary(t, "coap-server")
	enabled := qLibcoapMode(t) == qLibcoapEnabled
	addr := startQLibcoapServer(t, binary, enabled)
	payload := qPayload()
	for _, secure := range []bool{false, true} {
		for _, typ := range []message.Type{message.NonConfirmable, message.Confirmable} {
			for szx := blockwise.SZX16; szx <= blockwise.SZX1024; szx++ {
				t.Run(fmt.Sprintf("secure=%v/type=%v/block=%d", secure, typ, szx.Size()), func(t *testing.T) {
					testQLibcoapGoClient(t, binary, addr, payload, qLibcoapClientCase{enabled, secure, typ, szx})
				})
			}
		}
	}
	if !enabled {
		t.Run("QOnly-rejects-legacy-peer", func(t *testing.T) {
			cc := qDial(t, addr, false, blockwise.SZX1024, options.QBlockwiseOnly, new(qWireCounts))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := cc.Get(ctx, "/.well-known/core")
			if resp != nil {
				cc.ReleaseMessage(resp)
			}
			require.Error(t, err)
		})
	}
}

type qLibcoapClientCase struct {
	enabled bool
	secure  bool
	typ     message.Type
	szx     blockwise.SZX
}

func testQLibcoapGoClient(t *testing.T, binary, addr string, payload []byte, test qLibcoapClientCase) {
	enabled, secure, typ, szx := test.enabled, test.secure, test.typ, test.szx
	counts := new(qWireCounts)
	mode := options.QBlockwiseAuto
	if enabled {
		mode = options.QBlockwiseOnly
	}
	serverAddr := addr
	if !enabled && szx != blockwise.SZX1024 {
		serverAddr = startQLibcoapServer(t, binary, false, szx)
	}
	cc := qDial(t, qSecureAddr(t, serverAddr, secure), secure, szx, mode, counts)
	path := fmt.Sprintf("/q-%v-%v-%d", secure, typ, szx)
	var initialOptions []message.Option
	if !enabled && szx == blockwise.SZX16 {
		// libcoap's -b 16 maps to its zero/default sentinel;
		// explicitly request the sixteen-byte response size.
		initialOptions = []message.Option{{ID: message.Block2, Value: []byte{}}}
	}
	t.Run("upload", func(t *testing.T) {
		resp := qDo(t, cc, codes.PUT, path, payload, typ, initialOptions...)
		require.Contains(t, []codes.Code{codes.Created, codes.Changed}, resp.Code())
		got, err := resp.ReadBody()
		require.NoError(t, err)
		require.Equal(t, payload, got, "libcoap PUT echo must match exact binary body")
	})
	before := counts.q2.Load()
	t.Run("download", func(t *testing.T) {
		resp := qDo(t, cc, codes.GET, path, nil, typ, initialOptions...)
		require.Equal(t, codes.Content, resp.Code())
		got, err := resp.ReadBody()
		require.NoError(t, err)
		require.Equal(t, payload, got)
		if enabled {
			require.Greater(t, counts.q2.Load(), before, "must receive Q-Block2, not silently use legacy")
		} else {
			require.Zero(t, counts.q2.Load())
			require.Positive(t, counts.legacy.Load())
		}
	})
}

func startQGoServer(t *testing.T, secure bool, szx blockwise.SZX, payload []byte, counts *qWireCounts, calls *atomic.Int32, parameters ...options.QBlockwiseConfig) string {
	t.Helper()
	config := qGoServerOptions{mtu: client.DefaultConfig.MTU}
	if len(parameters) > 0 {
		config.blockwise = parameters[0]
	}
	return startQGoServerWithMTU(t, secure, szx, payload, counts, calls, config)
}

type qGoServerOptions struct {
	mtu       uint16
	blockwise options.QBlockwiseConfig
}

func startQGoServerWithMTU(t *testing.T, secure bool, szx blockwise.SZX, payload []byte, counts *qWireCounts, calls *atomic.Int32, config qGoServerOptions) string {
	t.Helper()
	router := mux.NewRouter()
	require.NoError(t, router.Handle("/q", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		calls.Add(1)
		switch r.Code() {
		case codes.PUT:
			got, err := r.ReadBody()
			if err != nil {
				t.Errorf("read Q request body: %v", err)
				return
			}
			if !bytes.Equal(payload, got) {
				t.Errorf("Q request differs: received %d bytes, want %d", len(got), len(payload))
			}
			if err := w.SetResponse(codes.Changed, message.AppOctets, bytes.NewReader(payload)); err != nil {
				t.Errorf("set Q response: %v", err)
			}
		case codes.GET:
			if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(payload)); err != nil {
				t.Errorf("set Q response: %v", err)
			}
		default:
			t.Errorf("unexpected method %v", r.Code())
		}
	})))
	served := make(chan error, 1)
	if secure {
		l, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", coapNet.NewDTLSServerOptions(
			piondtls.WithPSK(qLibcoapPSKCallback),
			piondtls.WithPSKIdentityHint([]byte("qblock-test")),
			piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256)))
		require.NoError(t, err)
		s := dtls.NewServer(options.WithMux(router),
			options.WithMTU(config.mtu),
			options.WithBlockwise(true, szx, 30*time.Second),
			options.WithQBlockwise(options.QBlockwiseAuto),
			options.WithQBlockwiseConfig(config.blockwise),
			options.WithRequestMonitor(counts.monitor),
		)
		go func() { served <- s.Serve(l) }()
		t.Cleanup(func() {
			s.Stop()
			require.NoError(t, l.Close())
			require.NoError(t, <-served)
		})
		return "coaps://" + l.Addr().String()
	}
	l, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	s := udp.NewServer(options.WithMux(router),
		options.WithMTU(config.mtu),
		options.WithBlockwise(true, szx, 30*time.Second),
		options.WithQBlockwise(options.QBlockwiseAuto), options.WithQBlockwiseConfig(config.blockwise), options.WithRequestMonitor(counts.monitor),
	)
	go func() { served <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Stop()
		require.NoError(t, l.Close())
		require.NoError(t, <-served)
	})
	return "coap://" + l.LocalAddr().String()
}

func runQLibcoapClient(t *testing.T, binary, uri string, secure bool, typ message.Type, szx blockwise.SZX, upload []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	output := filepath.Join(dir, "response.bin")
	args := []string{
		"-L", "7", "-b", strconv.FormatInt(szx.Size(), 10),
		"-B", "30", "-v", "7", "-o", output,
	}
	if upload == nil {
		args = append(args, "-m", "get")
	} else {
		input := filepath.Join(dir, "request.bin")
		require.NoError(t, os.WriteFile(input, upload, 0o600))
		args = append(args, "-m", "put", "-t", "42", "-f", input)
	}
	if typ == message.NonConfirmable {
		args = append(args, "-N")
	}
	if secure {
		args = append(args, "-u", "qblock-test", "-k", qLibcoapPSK)
	}
	args = append(args, uri)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	log := new(boundedCommandLog)
	cmd.Stdout, cmd.Stderr = log, log
	err := cmd.Run()
	require.NoError(t, err, "libcoap client log:\n%s", log.String())
	body, err := os.ReadFile(output)
	require.NoError(t, err, "libcoap client did not write response:\n%s", log.String())
	return body
}

func TestQBlockLibcoapGoServer(t *testing.T) {
	binary := qLibcoapBinary(t, "coap-client")
	if qLibcoapMode(t) == qLibcoapDisabled {
		// Still exercise the legacy-only reference against a Q-capable Go server.
		t.Run("legacy-client", func(t *testing.T) {
			counts := new(qWireCounts)
			calls := new(atomic.Int32)
			payload := qPayload()
			uri := startQGoServer(t, false, blockwise.SZX1024, payload, counts, calls)
			dir := t.TempDir()
			output := filepath.Join(dir, "legacy.bin")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-L", "3", "-b", "1024", "-o", output, uri+"/q")
			log := new(boundedCommandLog)
			cmd.Stdout, cmd.Stderr = log, log
			require.NoError(t, cmd.Run(), log.String())
			body, err := os.ReadFile(output)
			require.NoError(t, err)
			require.Equal(t, payload, body)
			require.Zero(t, counts.q1.Load()+counts.q2.Load())
			require.Positive(t, counts.legacy.Load())
			require.Equal(t, int32(1), calls.Load())
		})
		return
	}
	payload := qPayload()
	for _, secure := range []bool{false, true} {
		for _, typ := range []message.Type{message.NonConfirmable, message.Confirmable} {
			for szx := blockwise.SZX16; szx <= blockwise.SZX1024; szx++ {
				t.Run(fmt.Sprintf("secure=%v/type=%v/block=%d", secure, typ, szx.Size()), func(t *testing.T) {
					counts := new(qWireCounts)
					calls := new(atomic.Int32)
					uri := startQGoServer(t, secure, szx, payload, counts, calls)
					t.Run("upload", func(t *testing.T) {
						before := calls.Load()
						got := runQLibcoapClient(t, binary, uri+"/q", secure, typ, szx, payload)
						require.Equal(t, payload, got)
						require.Equal(t, before+1, calls.Load(), "application sees one assembled request")
						require.Positive(t, counts.q1.Load(), "must receive Q-Block1, not silently use legacy")
					})
					t.Run("download", func(t *testing.T) {
						before := calls.Load()
						got := runQLibcoapClient(t, binary, uri+"/q", secure, typ, szx, nil)
						require.Equal(t, payload, got)
						require.Equal(t, before+1, calls.Load(), "recovery must reuse retained representation")
						require.Positive(t, counts.q2.Load())
					})
					require.Zero(t, counts.legacy.Load(), "Q tests must not fall back")
				})
			}
		}
	}
}

func TestBoundedLibcoapLog(t *testing.T) {
	var log boundedCommandLog
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_, err := log.Write(bytes.Repeat([]byte("x"), 1024))
				if err != nil {
					t.Errorf("write bounded log: %v", err)
				}

				_ = log.String()
			}
		})
	}
	wg.Wait()
	require.Len(t, log.String(), 64*1024)
	require.NotContains(t, log.String(), "\x00")
	_, err := log.Write([]byte("last failure"))
	require.NoError(t, err)
	require.Len(t, log.String(), 64*1024)
	require.True(t, strings.HasSuffix(log.String(), "last failure"))
	_, err = log.Write(bytes.Repeat([]byte("z"), 128*1024))
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("z", 64*1024), log.String())
}

func TestQBlockLibcoapSelectiveRecovery(t *testing.T) {
	serverBinary := qLibcoapBinary(t, "coap-server")
	clientBinary := qLibcoapBinary(t, "coap-client")
	if qLibcoapMode(t) == qLibcoapDisabled {
		// Selective Q recovery cannot be exercised on a Q-disabled build.
		// The separate fallback tests still run without skipping this job.
		return
	}
	payload := qPayload()
	for _, goClient := range []bool{true, false} {
		for _, upload := range []bool{true, false} {
			t.Run(fmt.Sprintf("go-client=%v/upload=%v", goClient, upload), func(t *testing.T) {
				testQLibcoapSelectiveRecovery(t, serverBinary, clientBinary, payload, goClient, upload)
			})
		}
	}
}

func testQLibcoapSelectiveRecovery(t *testing.T, serverBinary, clientBinary string, payload []byte, goClient, upload bool) {
	const szx = blockwise.SZX128
	option := message.QBlock2
	if upload {
		option = message.QBlock1
	}
	var response []byte
	var relay *qDropRelay
	if goClient {
		serverAddr := startQLibcoapServer(t, serverBinary, true, szx)
		// Seed a replayable resource without fault injection.
		seed := qDial(t, serverAddr, false, szx, options.QBlockwiseOnly, new(qWireCounts))
		qDo(t, seed, codes.PUT, "/loss", payload, message.NonConfirmable)
		addr, fault := newQDropRelay(t, serverAddr, option, 3)
		relay = fault
		cc := qDial(t, addr, false, szx, options.QBlockwiseOnly, new(qWireCounts))
		method := codes.GET
		var body []byte
		if upload {
			method, body = codes.PUT, payload
		}
		resp := qDo(t, cc, method, "/loss", body, message.NonConfirmable)
		var err error
		response, err = resp.ReadBody()
		require.NoError(t, err)
	} else {
		calls := new(atomic.Int32)
		uri := startQGoServer(t, false, szx, payload, new(qWireCounts), calls)
		addr, fault := newQDropRelay(t, strings.TrimPrefix(uri, "coap://"), option, 3)
		relay = fault
		var body []byte
		if upload {
			body = payload
		}
		response = runQLibcoapClient(t, clientBinary, "coap://"+addr+"/q", false,
			message.NonConfirmable, szx, body)
		require.Equal(t, int32(1), calls.Load())
	}
	require.Equal(t, payload, response)
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.True(t, relay.dropped, "native test must drop a real Q payload")
	require.GreaterOrEqual(t, relay.observed[3], 2, "lost Q NUM must be recovered")
}
