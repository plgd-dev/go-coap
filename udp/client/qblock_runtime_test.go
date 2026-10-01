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
