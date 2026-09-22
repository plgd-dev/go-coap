package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type pairedQBlockWire struct {
	typ     message.Type
	code    codes.Code
	token   message.Token
	mid     int32
	options message.Options
	payload []byte
}

type pairedQBlockSession struct {
	qblockTestSession
	mu      sync.Mutex
	queue   []pairedQBlockWire
	history []pairedQBlockWire
}

func pairedQBlockSnapshot(cc *Conn) serverSnapshot {
	client := cc.qblockClient
	client.mu.Lock()
	defer client.mu.Unlock()
	snapshot := serverSnapshot{active: client.manager.Active()}
	manager := reflect.ValueOf(client.manager).Elem()
	snapshot.managerTokens = manager.FieldByName("byToken").Len()
	snapshot.managerBytes = manager.FieldByName("retained").Uint()
	if client.server != nil {
		snapshot.records = len(client.server.records)
		snapshot.metadataBytes = client.server.metadata
		snapshot.mids = len(client.server.byMID)
		for _, record := range client.server.records {
			snapshot.serverTokens += len(record.tokens)
		}
	}
	cc.tokenReservations.Range(func(_ uint64, _ tokenReservation) bool {
		snapshot.reservations++
		return true
	})
	return snapshot
}

func (s *pairedQBlockSession) WriteMessage(msg *pool.Message) error {
	options, err := msg.Options().Clone()
	if err != nil {
		return err
	}
	var payload []byte
	if body := msg.Body(); body != nil {
		payload, err = io.ReadAll(body)
		if err != nil {
			return err
		}
	}
	w := pairedQBlockWire{
		typ: msg.Type(), code: msg.Code(), token: bytes.Clone(msg.Token()), mid: msg.MessageID(), options: options, payload: bytes.Clone(payload),
	}
	s.mu.Lock()
	s.queue = append(s.queue, w)
	s.history = append(s.history, w)
	s.mu.Unlock()
	return nil
}

func (s *pairedQBlockSession) WriteMulticastMessage(*pool.Message, *net.UDPAddr, ...coapNet.MulticastOption) error {
	return nil
}

func (s *pairedQBlockSession) pop() (pairedQBlockWire, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return pairedQBlockWire{}, false
	}
	w := s.queue[0]
	s.queue = s.queue[1:]
	return w, true
}

func (s *pairedQBlockSession) historySnapshot() []pairedQBlockWire {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := make([]pairedQBlockWire, len(s.history))
	copy(history, s.history)
	return history
}

func deliverPairedQBlockWire(t *testing.T, target *Conn, w pairedQBlockWire) {
	t.Helper()
	msg := target.AcquireMessage(context.Background())
	msg.SetType(w.typ)
	msg.SetCode(w.code)
	msg.SetToken(w.token)
	msg.SetMessageID(w.mid)
	msg.ResetOptionsTo(w.options)
	if w.payload != nil {
		msg.SetBody(bytes.NewReader(w.payload))
	}
	if w.code < 32 {
		target.ProcessReceivedMessage(msg)
		return
	}
	handled := target.qblockClient.handle(msg)
	target.ReleaseMessage(msg)
	require.True(t, handled)
}

func drainPairedQBlock(t *testing.T, client, server *Conn, clientSession, serverSession *pairedQBlockSession, dropFirstQ2 bool) {
	t.Helper()
	dropped := false
	for i := 0; i < 100; i++ {
		progressed := false
		if wire, ok := clientSession.pop(); ok {
			deliverPairedQBlockWire(t, server, wire)
			progressed = true
		}
		if wire, ok := serverSession.pop(); ok {
			if dropFirstQ2 && !dropped && wire.options.HasOption(message.QBlock2) {
				dropped = true
			} else {
				deliverPairedQBlockWire(t, client, wire)
			}
			progressed = true
		}
		if !progressed {
			return
		}
	}
	t.Fatal("paired Q-Block queue did not drain")
}

func TestQBlockPrivatePairedRolesTrace(t *testing.T) {
	for _, method := range []struct {
		name string
		code codes.Code
	}{
		{name: "POST", code: codes.POST},
		{name: "PUT", code: codes.PUT},
	} {
		t.Run(method.name, func(t *testing.T) {
			runQBlockPrivatePairedRolesTrace(t, method.code)
		})
	}
}

func runQBlockPrivatePairedRolesTrace(t *testing.T, method codes.Code) {
	t.Helper()
	now := time.Unix(100, 0)
	managerConfig := serverLifecycleConfig()
	managerConfig.Transfer.MaxPayloads = 2
	clientSession := &pairedQBlockSession{qblockTestSession: qblockTestSession{ctx: context.Background()}}
	serverSession := &pairedQBlockSession{qblockTestSession: qblockTestSession{ctx: context.Background()}}
	clientCfg, serverCfg := DefaultConfig, DefaultConfig
	clientCfg.BlockwiseEnable, serverCfg.BlockwiseEnable = false, false
	clientCfg.BlockwiseSZX, serverCfg.BlockwiseSZX = blockwise.SZX16, blockwise.SZX16
	clientMID, serverMID := int32(1), int32(100)
	clientCfg.GetMID = func() int32 { value := clientMID; clientMID++; return value }
	serverCfg.GetMID = func() int32 { value := serverMID; serverMID++; return value }
	clientToken, serverToken := byte(10), byte(100)
	clientCfg.GetToken = func() (message.Token, error) { value := message.Token{clientToken}; clientToken++; return value, nil }
	serverCfg.GetToken = func() (message.Token, error) { value := message.Token{serverToken}; serverToken++; return value, nil }
	calls := 0
	serverCfg.Handler = func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
		calls++
		body, err := io.ReadAll(r.Body())
		require.NoError(t, err)
		require.Equal(t, bytes.Repeat([]byte{'u'}, 48), body)
		require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 48))))
	}
	client := NewConnWithOpts(clientSession, &clientCfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: func() time.Time { return now }, ScheduleMode: qblockScheduleManual}),
		withQBlockServer(qblockServerConfig{Retention: 10 * time.Second}),
	)
	server := NewConnWithOpts(serverSession, &serverCfg,
		withQBlockClient(qblockClientConfig{Manager: managerConfig, Now: func() time.Time { return now }, ScheduleMode: qblockScheduleManual}),
		withQBlockServer(qblockServerConfig{Retention: 10 * time.Second}),
	)
	t.Cleanup(clientSession.closeForTest)
	t.Cleanup(serverSession.closeForTest)

	original := message.Token{0xa1}
	received := make(chan []byte, 1)
	require.NoError(t, client.claimToken(original, tokenOwnerRequest))
	defer client.releaseToken(original, tokenOwnerRequest)
	_, loaded := client.tokenHandlerContainer.LoadOrStore(original.Hash(), func(_ *responsewriter.ResponseWriter[*Conn], msg *pool.Message) {
		body, err := io.ReadAll(msg.Body())
		require.NoError(t, err)
		received <- body
		client.ReleaseMessage(msg)
	})
	require.False(t, loaded)

	request := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(request)
	request.SetCode(method)
	request.SetToken(original)
	require.NoError(t, request.SetPath("/upload"))
	request.SetContentFormat(message.TextPlain)
	request.SetBody(bytes.NewReader(bytes.Repeat([]byte{'u'}, 48)))
	prepared, err := client.qblockClient.prepareQ1(request, nil)
	require.NoError(t, err)
	require.True(t, prepared)

	drainPairedQBlock(t, client, server, clientSession, serverSession, true)
	require.Equal(t, 1, calls)
	var duplicate pairedQBlockWire
	for _, wire := range clientSession.historySnapshot() {
		if !wire.options.HasOption(message.QBlock1) {
			continue
		}
		value, err := wire.options.GetUint32(message.QBlock1)
		require.NoError(t, err)
		block, err := qblock.DecodeBlock(value)
		require.NoError(t, err)
		if !block.More {
			duplicate = wire
			break
		}
	}
	require.NotEmpty(t, duplicate.token)
	duplicate.mid += 1000 // the same final Q1 under a distinct wire identity
	deliverPairedQBlockWire(t, server, duplicate)
	require.Equal(t, 1, calls)
	now = now.Add(managerConfig.Transfer.NonReceiveTimeout)
	client.CheckExpirations(now)
	drainPairedQBlock(t, client, server, clientSession, serverSession, false)
	now = now.Add(managerConfig.Transfer.NonTimeout)
	server.CheckExpirations(now)
	drainPairedQBlock(t, client, server, clientSession, serverSession, false)

	select {
	case body := <-received:
		require.Equal(t, bytes.Repeat([]byte{'r'}, 48), body)
	case <-time.After(time.Second):
		t.Fatal("client did not assemble repaired Q2 response")
	}
	client.releaseToken(original, tokenOwnerRequest)
	require.Equal(t, 1, calls)

	clientHistory := clientSession.historySnapshot()
	var uploadTag, controlTag, controlToken []byte
	for _, wire := range clientHistory {
		tag, _ := wire.options.GetBytes(message.RequestTag)
		if wire.options.HasOption(message.QBlock1) {
			uploadTag = tag
		}
		if wire.options.HasOption(message.QBlock2) {
			if controlToken == nil {
				controlTag = tag
				controlToken = wire.token
			}
		}
	}
	require.NotEmpty(t, uploadTag)
	require.Equal(t, uploadTag, controlTag)
	serverHistory := serverSession.historySnapshot()
	responseMIDs := make(map[int32]struct{})
	var responseTokens []message.Token
	for _, wire := range serverHistory {
		if wire.options.HasOption(message.QBlock2) {
			responseMIDs[wire.mid] = struct{}{}
			responseTokens = append(responseTokens, wire.token)
		}
	}
	require.GreaterOrEqual(t, len(responseMIDs), 3)
	require.GreaterOrEqual(t, len(responseTokens), 4)
	require.Equal(t, duplicate.token, responseTokens[0])
	require.Equal(t, duplicate.token, responseTokens[1])
	require.Equal(t, message.Token(controlToken), responseTokens[len(responseTokens)-1])

	// Keep an outbound Q1 live while the server retains the first response, so
	// both private roles must be cancelled by the close path.
	secondOriginal := message.Token{0xa2}
	require.NoError(t, client.claimToken(secondOriginal, tokenOwnerRequest))
	defer client.releaseToken(secondOriginal, tokenOwnerRequest)
	second := client.AcquireMessage(context.Background())
	defer client.ReleaseMessage(second)
	second.SetCode(method)
	second.SetToken(secondOriginal)
	require.NoError(t, second.SetPath("/second"))
	second.SetContentFormat(message.TextPlain)
	second.SetBody(bytes.NewReader(bytes.Repeat([]byte{'s'}, 32)))
	prepared, err = client.qblockClient.prepareQ1(second, nil)
	require.NoError(t, err)
	require.True(t, prepared)
	require.Positive(t, client.qblockClient.active())
	require.Positive(t, server.qblockClient.active())
	client.releaseToken(secondOriginal, tokenOwnerRequest)
	clientSession.closeForTest()
	serverSession.closeForTest()
	require.Equal(t, serverSnapshot{}, pairedQBlockSnapshot(client))
	require.Equal(t, serverSnapshot{}, pairedQBlockSnapshot(server))
}

func serverLifecycleConfig() qblock.ManagerConfig {
	cfg := qblock.DefaultManagerConfig()
	cfg.Transfer.Lifetime = 10 * time.Second
	return cfg
}

func TestQBlockServerLifecycle(t *testing.T) {
	t.Run("expired handler settles retention when it returns", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			close(entered)
			<-release
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("late"))))
		})
		go func() {
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			close(returned)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("handler did not start")
		}

		h.advance(10 * time.Second)
		require.Zero(t, h.snapshot().active)
		h.advance(5 * time.Second)
		close(release)
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("handler did not return")
		}

		var exists, terminal, handlerRunning bool
		var expires time.Time
		func() {
			h.cc.qblockClient.mu.Lock()
			defer h.cc.qblockClient.mu.Unlock()
			for _, record := range h.cc.qblockClient.server.records {
				exists = true
				terminal = record.terminal
				handlerRunning = record.handlerRunning
				expires = record.expires
			}
		}()
		require.True(t, exists, "terminal duplicate-suppression record was released early")
		require.True(t, terminal)
		require.False(t, handlerRunning)
		require.Equal(t, h.now.Add(10*time.Second), expires)

		h.advance(9 * time.Second)
		require.Equal(t, 1, h.snapshot().records)
		h.advance(time.Second)
		require.Zero(t, h.snapshot().records)
	})

	t.Run("terminal record frees capacity exactly at retention", func(t *testing.T) {
		calls := 0
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second, MaxRecords: 1}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			calls++
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
		})
		h.session.writeErr = io.ErrClosedPipe
		h.ingest(h.q1(t, 1, 0, false, 4, "body"))
		require.Equal(t, 1, calls)
		require.Equal(t, 1, h.snapshot().records)

		blocked := h.q1(t, 2, 0, false, 4, "body")
		require.NoError(t, blocked.SetPath("/other"))
		h.ingest(blocked)
		require.Equal(t, 1, calls)
		full := h.snapshot()
		require.Equal(t, 1, full.records)
		require.Positive(t, full.metadataBytes)

		h.advance(10 * time.Second)
		require.Zero(t, h.snapshot().records)
		require.Zero(t, h.snapshot().metadataBytes)
		h.session.writeErr = nil
		accepted := h.q1(t, 3, 0, false, 4, "body")
		require.NoError(t, accepted.SetPath("/other"))
		h.ingest(accepted)
		require.Equal(t, 2, calls)
	})

	t.Run("reset cancels only its server transfer", func(t *testing.T) {
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader(bytes.Repeat([]byte{'r'}, 32))))
		})
		h.ingest(h.q1(t, 1, 0, false, 4, "body"))
		writes := h.session.writesSnapshot()
		require.NotEmpty(t, writes)
		before := h.snapshot()
		require.Positive(t, before.active)

		reset := h.cc.AcquireMessage(context.Background())
		defer h.cc.ReleaseMessage(reset)
		reset.SetType(message.Reset)
		reset.SetMessageID(writes[0].mid)
		require.True(t, h.cc.qblockClient.handle(reset))
		after := h.snapshot()
		require.Zero(t, after.active)
		require.Zero(t, after.managerTokens)
		require.Zero(t, after.serverTokens)
		require.Positive(t, before.mids)
		require.Zero(t, after.mids)

		unknown := h.cc.AcquireMessage(context.Background())
		defer h.cc.ReleaseMessage(unknown)
		unknown.SetType(message.Reset)
		unknown.SetMessageID(999)
		require.False(t, h.cc.qblockClient.handle(unknown))
		require.Equal(t, after, h.snapshot())
	})

	t.Run("blocked handler cannot revive a closed record", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		calls := 0
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			calls++
			close(entered)
			<-release
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("late"))))
		})
		go func() {
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			close(returned)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("handler did not start")
		}

		h.advance(10 * time.Second)
		h.ingest(h.q1(t, 2, 0, false, 4, "body"))
		require.Equal(t, 1, calls)
		h.session.closeForTest()
		afterClose := h.snapshot()
		require.Zero(t, afterClose.active)
		require.Zero(t, afterClose.records)
		require.Zero(t, afterClose.managerTokens)
		require.Zero(t, afterClose.managerBytes)
		require.Zero(t, afterClose.serverTokens)
		require.Zero(t, afterClose.mids)
		require.Zero(t, afterClose.metadataBytes)

		close(release)
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("handler did not return")
		}
		require.Empty(t, h.session.writesSnapshot())
	})

	t.Run("close cleans records before a blocked write drains", func(t *testing.T) {
		h := newServerHarness(t, serverLifecycleConfig(), qblockServerConfig{Retention: 10 * time.Second}, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			require.NoError(t, w.SetResponse(codes.Changed, message.TextPlain, bytes.NewReader([]byte("response"))))
		})
		h.session.firstWriteStarted = make(chan struct{}, 1)
		h.session.releaseFirstWrite = make(chan struct{})
		ingested := make(chan struct{})
		go func() {
			h.ingest(h.q1(t, 1, 0, false, 4, "body"))
			close(ingested)
		}()
		select {
		case <-h.session.firstWriteStarted:
		case <-time.After(time.Second):
			t.Fatal("response write did not block")
		}

		closed := make(chan struct{})
		go func() {
			h.session.closeForTest()
			close(closed)
		}()
		require.Eventually(t, func() bool {
			snapshot := h.snapshot()
			return snapshot.active == 0 && snapshot.records == 0 && snapshot.managerTokens == 0 && snapshot.managerBytes == 0 && snapshot.serverTokens == 0 && snapshot.mids == 0 && snapshot.metadataBytes == 0
		}, time.Second, time.Millisecond)
		close(h.session.releaseFirstWrite)
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatal("close did not return")
		}
		select {
		case <-ingested:
		case <-time.After(time.Second):
			t.Fatal("ingest did not return")
		}
	})
}
