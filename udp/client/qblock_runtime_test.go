package client

import (
	"context"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestQBlockRuntimeServerOnlyConstruction(t *testing.T) {
	runtime, err := NewQBlockServerRuntime(qblock.DefaultServerConfig())
	require.NoError(t, err)
	defer runtime.Close()
	session := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
	cfg := DefaultConfig
	cc, err := runtime.NewConn(session, &cfg)
	require.NoError(t, err)
	defer session.closeForTest()
	require.NotNil(t, cc.qblockClient.server)
	req := newPrivateQBlockClientGET(t, cc, []byte{1})
	defer cc.ReleaseMessage(req)
	require.False(t, cc.qblockClient.canPrepare(req))
	runtime.Close()
	_, err = runtime.NewConn(session, &cfg)
	require.Error(t, err)
}

func TestQBlockRuntimeRejectsOwnedBudgetBeforeConnections(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.MaxOwnedBytes = 1
	_, err := NewQBlockServerRuntime(cfg)
	require.Error(t, err, "invalid budget before server starts")
}
func TestQBlockRuntimeRejectsDerivedPacingOverflow(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	cfg.Manager.Transfer.NonTimeout = 8 * time.Second
	cfg.Manager.Transfer.NonReceiveTimeout = 13 * time.Second
	cfg.Manager.Transfer.NonMaxRetransmit = 30
	cfg.Manager.Transfer.Lifetime = 24 * time.Hour
	cfg.Retention = cfg.Manager.Transfer.Lifetime
	require.NoError(t, cfg.Manager.Validate())
	_, err := NewQBlockServerRuntime(cfg)
	require.Error(t, err)
}
func TestQBlockRuntimeTransportBudgetValidation(t *testing.T) {
	cfg := qblock.DefaultServerConfig()
	runtime, err := NewQBlockServerRuntime(cfg)
	require.NoError(t, err)
	probe := &qblockClient{managerConfig: cfg.Manager, datagramLimit: uint32(DefaultMTU), maxMIDEntries: cfg.MaxMIDEntries, pacingConfig: qblockPacingConfig{MaxIntentBytes: cfg.MaxIntentBytes}, server: &qblockServer{config: qblockServerConfig{MaxRecords: cfg.MaxRecords, MaxMetadataBytes: cfg.MaxMetadataBytes}}}
	require.NoError(t, probe.initOwnedBudget())
	runtime.config.MaxOwnedBytes = probe.ownedBudget.floor + max(probe.ownedBudget.clientCost, probe.ownedBudget.serverCost)
	require.NoError(t, runtime.ValidateTransport(uint32(DefaultMTU)))
	require.Error(t, runtime.ValidateTransport(65535))
}

func TestQBlockCombinedRuntime(t *testing.T) {
	outbound := qblock.DefaultClientConfig()
	inbound := qblock.DefaultServerConfig()
	r, err := NewQBlockRuntime(&outbound, &inbound)
	require.NoError(t, err)
	defer r.Close()
	inbound.MaxMIDEntries = 1
	_, err = NewQBlockRuntime(&outbound, &inbound)
	require.Error(t, err)
	r2, err := NewQBlockRuntime(&outbound, nil)
	require.NoError(t, err)
	defer r2.Close()
}

func TestQBlockProductionJitter(t *testing.T) {
	c := publicQBlockClientConfig(qblock.DefaultClientConfig(), nil)
	require.Equal(t, qblockScheduleAutomatic, c.ScheduleMode)
	require.NotNil(t, c.Clock)
	values := map[float64]bool{}
	for range 16 {
		v := c.Jitter()
		require.GreaterOrEqual(t, v, float64(0))
		require.Less(t, v, float64(1))
		values[v] = true
	}
	require.Greater(t, len(values), 1)
}

func TestQBlockCombinedRuntimeRoles(t *testing.T) {
	for _, inbound := range []bool{false, true} {
		q := qblock.DefaultClientConfig()
		server := qblock.DefaultServerConfig()
		var role *qblock.ServerConfig
		if inbound {
			role = &server
		}
		r, err := NewQBlockRuntime(&q, role)
		require.NoError(t, err)
		session := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
		cfg := DefaultConfig
		cc, err := r.NewConn(session, &cfg)
		require.NoError(t, err)
		require.NotNil(t, cc.qblockConfig)
		require.Equal(t, inbound, cc.qblockClient.server != nil)
		require.Same(t, r.domain, cc.qblockClient.endpoint.domain)
		req := newPrivateQBlockClientGET(t, cc, []byte{1})
		selected, err := cc.selectQBlock(req)
		require.NoError(t, err)
		require.False(t, selected)
		cc.ReleaseMessage(req)
		session.closeForTest()
		r.Close()
	}
}

func TestQBlockCombinedInvalidInbound(t *testing.T) {
	out := qblock.DefaultClientConfig()
	for _, change := range []func(*qblock.ServerConfig){func(c *qblock.ServerConfig) { c.Retention = -time.Second }, func(c *qblock.ServerConfig) { c.MaxRecords = 0 }, func(c *qblock.ServerConfig) { c.MaxMetadataBytes = 0 }} {
		in := qblock.DefaultServerConfig()
		change(&in)
		_, err := NewQBlockRuntime(&out, &in)
		require.Error(t, err)
	}
}
func TestQBlockRuntimeRejectsStoredInitError(t *testing.T) {
	out := qblock.DefaultClientConfig()
	r, err := NewQBlockRuntime(&out, nil)
	require.NoError(t, err)
	defer r.Close()
	s := &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}
	defer s.closeForTest()
	cfg := DefaultConfig
	cfg.BlockwiseSZX = 7
	cc, err := r.NewConn(s, &cfg)
	require.Error(t, err)
	require.Nil(t, cc)
}

func TestQBlockAcceptedBudgetMatchesSession(t *testing.T) {
	q := qblock.DefaultClientConfig()
	probe := &qblockClient{managerConfig: q.Manager, datagramLimit: 64, maxMIDEntries: q.MaxMIDEntries, pacingConfig: qblockPacingConfig{MaxIntentBytes: q.MaxIntentBytes}}
	require.NoError(t, probe.initOwnedBudget())
	q.MaxOwnedBytes = probe.ownedBudget.floor + probe.ownedBudget.clientCost
	r, err := NewQBlockRuntime(&q, nil)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, r.ValidateTransport(64))
	require.Error(t, r.ValidateTransport(128))
	cfg := DefaultConfig
	cfg.MTU = 128
	cfg.MaxMessageSize = 64
	s := &capBudgetSession{qblockTestSession: &qblockTestSession{ctx: context.Background(), remoteAddr: endpointPeer(123)}}
	defer s.closeForTest()
	cc, err := r.NewConn(s, &cfg)
	require.NoError(t, err)
	require.NoError(t, cc.InitializationError())
}

type capBudgetSession struct{ *qblockTestSession }

func (s *capBudgetSession) MaxMessageSize() uint32 { return 64 }
