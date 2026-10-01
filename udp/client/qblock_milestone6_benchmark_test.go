package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

const (
	qblockM6SmallBody = 8 << 10
	qblockM6LargeBody = 256 << 10
	qblockM6DropBlock = 3
)

// BenchmarkQBlockMilestone6WireMatrix measures complete Q2 GETs over loopback
// UDP. It deliberately reports Q manager reservations separately for each
// peer and adapter-owned reservations separately from process memory.
func BenchmarkQBlockMilestone6WireMatrix(b *testing.B) {
	for _, bodySize := range []struct {
		name string
		size int
	}{
		{name: "small", size: qblockM6SmallBody},
		{name: "large", size: qblockM6LargeBody},
	} {
		for _, loss := range []bool{false, true} {
			name := bodySize.name + "/no-loss"
			if loss {
				name = bodySize.name + "/drop-response-block-3"
			}
			b.Run(name, func(b *testing.B) {
				b.SetBytes(int64(bodySize.size))
				body := qblockM6PatternedBody(bodySize.size)
				wantHash := sha256.Sum256(body)
				var totalElapsed time.Duration
				var clientManagerPeak, serverManagerPeak uint64
				var clientOwnedPeak, serverOwnedPeak uint64
				var totalDrops uint32

				b.StopTimer()
				for i := 0; i < b.N; i++ {
					fixture, err := newQBlockM6WireFixture(body, loss)
					if err != nil {
						b.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					supported, err := fixture.client.ProbeQBlock(ctx, "/probe")
					if err != nil || !supported {
						clientBudget := qblockM6BudgetDescription(fixture.client.qblockClient.ownedBudget)
						serverBudget := qblockM6BudgetDescription(fixture.server.qblockClient.ownedBudget)
						clientReported, serverReported := fixture.clientSession.handlerError(), fixture.serverSession.handlerError()
						cancel()
						fixture.close()
						b.Fatalf("explicit probe supported=%t err=%v client-budget=%s server-budget=%s client-reported=%v server-reported=%v", supported, err, clientBudget, serverBudget, clientReported, serverReported)
					}
					if loss {
						fixture.serverSession.dropArmed.Store(true)
					}

					fixture.metrics.reset()
					fixture.clientSession.sampleNow()
					started := time.Now()
					b.StartTimer()
					req, err := fixture.client.NewGetRequest(ctx, "/body")
					if err != nil {
						b.StopTimer()
						cancel()
						fixture.close()
						b.Fatal(err)
					}
					resp, doErr := fixture.client.Do(req)
					fixture.client.ReleaseMessage(req)
					if doErr != nil {
						b.StopTimer()
						cancel()
						fixture.close()
						b.Fatalf("Q2 GET: %v", doErr)
					}
					var got []byte
					if resp.Body() != nil {
						got, err = io.ReadAll(resp.Body())
					}
					fixture.client.ReleaseMessage(resp)
					elapsed := time.Since(started)
					b.StopTimer()
					cancel()

					if err != nil {
						fixture.close()
						b.Fatal(err)
					}
					if !bytes.Equal(got, body) {
						fixture.close()
						b.Fatalf("body mismatch: got=%d want=%d", len(got), len(body))
					}
					if hash := sha256.Sum256(got); hash != wantHash {
						fixture.close()
						b.Fatalf("body hash mismatch: got=%x want=%x", hash, wantHash)
					}
					wantDrops := uint32(0)
					if loss {
						wantDrops = 1
					}
					if gotDrops := fixture.serverSession.dropCount.Load(); gotDrops != wantDrops {
						fixture.close()
						b.Fatalf("dropped Q2 response blocks=%d want=%d", gotDrops, wantDrops)
					}
					if fixture.classicOptions.Load() {
						fixture.close()
						b.Fatal("classic Block1/Block2 option observed")
					}

					clientManagerPeak = max(clientManagerPeak, fixture.metrics.clientManager.Load())
					serverManagerPeak = max(serverManagerPeak, fixture.metrics.serverManager.Load())
					clientOwnedPeak = max(clientOwnedPeak, fixture.metrics.clientOwned.Load())
					serverOwnedPeak = max(serverOwnedPeak, fixture.metrics.serverOwned.Load())
					totalDrops += fixture.serverSession.dropCount.Load()
					totalElapsed += elapsed
					fixture.close()
				}
				if totalElapsed <= 0 {
					b.Fatal("benchmark elapsed time was not positive")
				}
				mibPerSecond := float64(bodySize.size*b.N) / totalElapsed.Seconds() / (1 << 20)
				transferPerOperation := totalElapsed / time.Duration(b.N)
				b.Logf("qblock-m6 transfer-ns/op=%d throughput-MiB/s=%.6f", transferPerOperation.Nanoseconds(), mibPerSecond)
				b.ReportMetric(mibPerSecond, "MiB/s")
				b.ReportMetric(float64(clientManagerPeak), "client-manager-reserved-B")
				b.ReportMetric(float64(serverManagerPeak), "server-manager-reserved-B")
				b.ReportMetric(float64(clientOwnedPeak), "client-adapter-reserved-B")
				b.ReportMetric(float64(serverOwnedPeak), "server-adapter-reserved-B")
				b.ReportMetric(float64(totalDrops), "dropped-Q2-blocks")
			})
		}
	}
}

type qblockM6WireFixture struct {
	client         *Conn
	server         *Conn
	runtime        *QBlockServerRuntime
	clientSession  *qblockM6UDPSession
	serverSession  *qblockM6UDPSession
	metrics        *qblockM6Metrics
	clientRunDone  chan error
	serverRunDone  chan error
	classicOptions atomic.Bool
}

func newQBlockM6WireFixture(body []byte, drop bool) (*qblockM6WireFixture, error) {
	serverSocket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	clientSocket, err := net.DialUDP("udp4", nil, serverSocket.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = serverSocket.Close()
		return nil, err
	}
	clientCtx, clientCancel := context.WithCancel(context.Background())
	serverCtx, serverCancel := context.WithCancel(context.Background())
	clientSession := newQBlockM6UDPSession(clientCtx, clientCancel, clientSocket, clientSocket.RemoteAddr().(*net.UDPAddr), false, false)
	serverSession := newQBlockM6UDPSession(serverCtx, serverCancel, serverSocket, clientSocket.LocalAddr().(*net.UDPAddr), true, drop)
	fixture := &qblockM6WireFixture{
		clientSession: clientSession,
		serverSession: serverSession,
		metrics:       &qblockM6Metrics{},
	}
	clientSession.classicOptions = &fixture.classicOptions
	serverSession.classicOptions = &fixture.classicOptions

	clientQ := qblock.DefaultClientConfig()
	clientQ.Mode = qblock.Require
	clientQ.Manager.Transfer.MaxBodySize = uint32(len(body))
	clientQ.Manager.Transfer.MaxPayloads = 10
	clientQ.Manager.Transfer.NonTimeout = 100 * time.Millisecond
	clientQ.Manager.Transfer.NonReceiveTimeout = 1150 * time.Millisecond
	clientQ.Manager.Transfer.Lifetime = 30 * time.Second
	clientQ.ProbingRate = 65536
	clientQ.NonProbingWait = 10 * time.Millisecond
	clientConfig := DefaultConfig
	clientConfig.QBlock = &clientQ
	clientConfig.BlockwiseSZX = blockwise.SZX1024
	clientConfig.Errors = func(err error) { clientSession.reportError(err) }
	fixture.client = NewConnWithOpts(clientSession, &clientConfig)
	if err := fixture.client.InitializationError(); err != nil {
		fixture.close()
		return nil, fmt.Errorf("construct benchmark client: %w", err)
	}

	serverQ := qblock.DefaultServerConfig()
	serverQ.Manager.Transfer.MaxBodySize = uint32(len(body))
	serverQ.Manager.Transfer.MaxPayloads = 10
	serverQ.Manager.Transfer.NonTimeout = 100 * time.Millisecond
	serverQ.Manager.Transfer.NonReceiveTimeout = 1150 * time.Millisecond
	serverQ.Manager.Transfer.Lifetime = 30 * time.Second
	serverQ.Retention = 30 * time.Second
	serverQ.ProbingRate = 65536
	serverQ.NonProbingWait = 10 * time.Millisecond
	fixture.runtime, err = NewQBlockRuntime(nil, &serverQ)
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("construct benchmark server runtime: %w", err)
	}
	serverConfig := DefaultConfig
	serverConfig.BlockwiseSZX = blockwise.SZX1024
	serverConfig.Errors = func(err error) { serverSession.reportError(err) }
	serverConfig.Handler = func(w *responsewriter.ResponseWriter[*Conn], req *pool.Message) {
		responseBody := body
		if req.Type() == message.Confirmable && req.HasOption(message.QBlock2) {
			responseBody = []byte("probe")
		}
		if err := w.SetResponse(codes.Content, message.AppOctets, bytes.NewReader(responseBody)); err != nil {
			serverSession.reportError(err)
		}
	}
	fixture.server, err = fixture.runtime.NewConn(serverSession, &serverConfig)
	if err != nil {
		fixture.close()
		return nil, fmt.Errorf("construct benchmark server connection: %w", err)
	}
	clientSession.sample = fixture.metrics.observe
	serverSession.sample = fixture.metrics.observe
	serverSession.metricConns = [2]*Conn{fixture.client, fixture.server}
	clientSession.metricConns = [2]*Conn{fixture.client, fixture.server}
	fixture.clientRunDone = make(chan error, 1)
	fixture.serverRunDone = make(chan error, 1)
	go func() { fixture.serverRunDone <- fixture.server.Run() }()
	go func() { fixture.clientRunDone <- fixture.client.Run() }()
	return fixture, nil
}

func (f *qblockM6WireFixture) close() {
	if f.client != nil {
		_ = f.client.Close()
	}
	if f.server != nil {
		_ = f.server.Close()
	}
	if f.runtime != nil {
		f.runtime.Close()
	}
	if f.clientRunDone != nil {
		select {
		case <-f.clientRunDone:
		case <-time.After(time.Second):
		}
	}
	if f.serverRunDone != nil {
		select {
		case <-f.serverRunDone:
		case <-time.After(time.Second):
		}
	}
}

func qblockM6PatternedBody(size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte((i*31 + 7) & 0xff)
	}
	return body
}

type qblockM6UDPSession struct {
	ctxMu          sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	socket         *net.UDPConn
	serverSide     bool
	dropResponse   bool
	dropArmed      atomic.Bool
	dropCount      atomic.Uint32
	classicOptions *atomic.Bool
	remoteMu       sync.RWMutex
	remote         *net.UDPAddr
	done           chan struct{}
	closeOnce      sync.Once
	hooksMu        sync.Mutex
	hooks          []EventFunc
	sample         func([2]*Conn)
	metricConns    [2]*Conn
	errorMu        sync.Mutex
	lastHandlerErr error
}

func newQBlockM6UDPSession(ctx context.Context, cancel context.CancelFunc, socket *net.UDPConn, remote *net.UDPAddr, serverSide, dropResponse bool) *qblockM6UDPSession {
	s := &qblockM6UDPSession{ctx: ctx, cancel: cancel, socket: socket, serverSide: serverSide, dropResponse: dropResponse, remote: remote, done: make(chan struct{})}
	if dropResponse {
		s.dropArmed.Store(true)
	}
	return s
}

func (s *qblockM6UDPSession) Context() context.Context {
	s.ctxMu.RLock()
	defer s.ctxMu.RUnlock()
	return s.ctx
}

func (s *qblockM6UDPSession) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.cancel()
		closeErr = s.socket.Close()
		close(s.done)
		s.hooksMu.Lock()
		hooks := append([]EventFunc(nil), s.hooks...)
		s.hooks = nil
		s.hooksMu.Unlock()
		for _, hook := range hooks {
			hook()
		}
	})
	if errors.Is(closeErr, net.ErrClosed) {
		return nil
	}
	return closeErr
}

func (s *qblockM6UDPSession) MaxMessageSize() uint32 { return 65535 }

func (s *qblockM6UDPSession) RemoteAddr() net.Addr {
	s.remoteMu.RLock()
	defer s.remoteMu.RUnlock()
	if s.remote != nil {
		return s.remote
	}
	return &net.UDPAddr{}
}

func (s *qblockM6UDPSession) LocalAddr() net.Addr   { return s.socket.LocalAddr() }
func (s *qblockM6UDPSession) NetConn() net.Conn     { return s.socket }
func (s *qblockM6UDPSession) Done() <-chan struct{} { return s.done }

func (s *qblockM6UDPSession) WriteMessage(msg *pool.Message) error {
	if msg.HasOption(message.Block1) || msg.HasOption(message.Block2) {
		s.classicOptions.Store(true)
	}
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	if s.shouldDropQ2Response(msg) {
		s.dropCount.Add(1)
		s.sampleNow()
		return nil
	}
	if s.serverSide {
		s.remoteMu.RLock()
		remote := s.remote
		s.remoteMu.RUnlock()
		if remote == nil {
			return errors.New("loopback server has no client address")
		}
		_, err = s.socket.WriteToUDP(wire, remote)
	} else {
		_, err = s.socket.Write(wire)
	}
	if err == nil {
		s.sampleNow()
	}
	return err
}

func (s *qblockM6UDPSession) shouldDropQ2Response(msg *pool.Message) bool {
	if !s.serverSide || !s.dropResponse || msg.Type() != message.NonConfirmable || !msg.HasOption(message.QBlock2) || !s.dropArmed.Load() {
		return false
	}
	value, err := msg.GetOptionUint32(message.QBlock2)
	if err != nil {
		return false
	}
	block, err := qblock.DecodeBlock(value)
	if err != nil || block.Number != qblockM6DropBlock || !s.dropArmed.CompareAndSwap(true, false) {
		return false
	}
	return true
}

func (s *qblockM6UDPSession) WriteMulticastMessage(msg *pool.Message, address *net.UDPAddr, _ ...coapNet.MulticastOption) error {
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return err
	}
	_, err = s.socket.WriteToUDP(wire, address)
	return err
}

func (s *qblockM6UDPSession) Run(conn *Conn) error {
	buffer := make([]byte, int(s.MaxMessageSize()))
	for {
		var n int
		var remote *net.UDPAddr
		var err error
		if s.serverSide {
			n, remote, err = s.socket.ReadFromUDP(buffer)
			if err == nil {
				s.remoteMu.Lock()
				s.remote = remote
				s.remoteMu.Unlock()
			}
		} else {
			n, err = s.socket.Read(buffer)
		}
		if err != nil {
			if s.Context().Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := conn.Process(nil, buffer[:n]); err != nil {
			return err
		}
		s.sampleNow()
	}
}

func (s *qblockM6UDPSession) AddOnClose(hook EventFunc) {
	s.hooksMu.Lock()
	select {
	case <-s.done:
		s.hooksMu.Unlock()
		hook()
	default:
		s.hooks = append(s.hooks, hook)
		s.hooksMu.Unlock()
	}
}

func (s *qblockM6UDPSession) SetContextValue(key interface{}, value interface{}) {
	s.ctxMu.Lock()
	s.ctx = context.WithValue(s.ctx, key, value)
	s.ctxMu.Unlock()
}

func (s *qblockM6UDPSession) sampleNow() {
	if s.sample != nil {
		s.sample(s.metricConns)
	}
}

func (s *qblockM6UDPSession) reportError(err error) {
	s.errorMu.Lock()
	s.lastHandlerErr = err
	s.errorMu.Unlock()
}

func (s *qblockM6UDPSession) handlerError() error {
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	return s.lastHandlerErr
}

type qblockM6Metrics struct {
	clientManager atomic.Uint64
	serverManager atomic.Uint64
	clientOwned   atomic.Uint64
	serverOwned   atomic.Uint64
}

func (m *qblockM6Metrics) observe(conns [2]*Conn) {
	updateQBlockM6Max(&m.clientManager, qblockM6ManagerRetained(conns[0]))
	updateQBlockM6Max(&m.serverManager, qblockM6ManagerRetained(conns[1]))
	updateQBlockM6Max(&m.clientOwned, qblockM6AdapterReserved(conns[0]))
	updateQBlockM6Max(&m.serverOwned, qblockM6AdapterReserved(conns[1]))
}

func (m *qblockM6Metrics) reset() {
	m.clientManager.Store(0)
	m.serverManager.Store(0)
	m.clientOwned.Store(0)
	m.serverOwned.Store(0)
}

func updateQBlockM6Max(value *atomic.Uint64, next uint64) {
	for current := value.Load(); next > current; current = value.Load() {
		if value.CompareAndSwap(current, next) {
			return
		}
	}
}

func qblockM6ManagerRetained(conn *Conn) uint64 {
	if conn == nil || conn.qblockClient == nil || conn.qblockClient.manager == nil {
		return 0
	}
	conn.qblockClient.mu.Lock()
	defer conn.qblockClient.mu.Unlock()
	value := reflect.ValueOf(conn.qblockClient.manager).Elem().FieldByName("retained")
	if !value.IsValid() || value.Kind() != reflect.Uint64 {
		return 0
	}
	return value.Uint()
}

func qblockM6AdapterReserved(conn *Conn) uint64 {
	if conn == nil || conn.qblockClient == nil || conn.qblockClient.ownedBudget == nil {
		return 0
	}
	budget := conn.qblockClient.ownedBudget
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

func qblockM6BudgetDescription(budget *qblockOwnedBudget) string {
	if budget == nil {
		return "<nil>"
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return fmt.Sprintf("used=%d floor=%d clientCost=%d serverCost=%d limit=%d", budget.used, budget.floor, budget.clientCost, budget.serverCost, budget.limit)
}
