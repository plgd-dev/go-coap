package client

import (
	"errors"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

// QBlockServerRuntime bridges UDP server construction to shared endpoint state.
// Applications normally use options.WithQBlockServer on udp/server.New.
type QBlockServerRuntime struct {
	config qblock.ServerConfig
	domain *qblockEndpointDomain
}

func NewQBlockServerRuntime(cfg qblock.ServerConfig) (*QBlockServerRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, err := normalizeQBlockPacingConfig(&qblockPacingConfig{ProbingRate: cfg.ProbingRate, NonProbingWait: cfg.NonProbingWait, MaxIntentBytes: cfg.MaxIntentBytes}, cfg.Manager); err != nil {
		return nil, err
	}

	runtime := &QBlockServerRuntime{config: cfg, domain: newQBlockEndpointDomain(realQBlockClock{}, cfg.ProbingRate, cfg.MaxPeers, cfg.MaxEndpointMembers)}
	if err := runtime.ValidateTransport(1); err != nil {
		return nil, err
	}
	return runtime, nil
}
func (r *QBlockServerRuntime) NewConn(session Session, cfg *Config, opts ...Option) (*Conn, error) {
	c := r.config
	opts = append(opts, withQBlockClient(qblockClientConfig{Manager: c.Manager, Clock: r.domain.clock, Pacing: &qblockPacingConfig{ProbingRate: c.ProbingRate, NonProbingWait: c.NonProbingWait, MaxIntentBytes: c.MaxIntentBytes}, Endpoint: r.domain, ScheduleMode: qblockScheduleAutomatic, MaxOwnedBytes: c.MaxOwnedBytes, MaxMIDEntries: c.MaxMIDEntries}), withQBlockServer(qblockServerConfig{Retention: c.Retention, MaxRecords: c.MaxRecords, MaxMetadataBytes: c.MaxMetadataBytes}))
	cc := NewConnWithOpts(session, cfg, opts...)
	cc.qblockClient.serverOnly = true
	if err := cc.qblockClient.initErr; err != nil {
		cc.qblockClient.close()
		return nil, errors.Join(err, session.Close())
	}
	return cc, nil
}
func (r *QBlockServerRuntime) Close() { r.domain.close() }
func (r *QBlockServerRuntime) Prune(now time.Time) {
	r.domain.mu.Lock()
	r.domain.pruneLocked(now)
	r.domain.mu.Unlock()
}

// ValidateTransport checks adapter reservations against the final UDP payload cap.
func (r *QBlockServerRuntime) ValidateTransport(datagram uint32) error {
	cfg := r.config
	if datagram == 0 {
		return qblock.ErrLimitExceeded
	}
	probe := &qblockClient{managerConfig: cfg.Manager, datagramLimit: datagram, maxMIDEntries: cfg.MaxMIDEntries, maxOwnedBytes: cfg.MaxOwnedBytes, pacingConfig: qblockPacingConfig{MaxIntentBytes: cfg.MaxIntentBytes}}
	probe.server = &qblockServer{config: qblockServerConfig{MaxRecords: cfg.MaxRecords, MaxMetadataBytes: cfg.MaxMetadataBytes}}
	return probe.initOwnedBudget()
}
