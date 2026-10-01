package client

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

// QBlockServerRuntime bridges UDP/DTLS server construction to shared endpoint state.
// Applications normally use options.WithQBlockServer on udp/server.New or dtls/server.New.
type QBlockServerRuntime struct {
	config       qblock.ServerConfig
	clientConfig *qblock.ClientConfig
	inbound      bool
	domain       *qblockEndpointDomain
}

func NewQBlockServerRuntime(cfg qblock.ServerConfig) (*QBlockServerRuntime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if _, err := normalizeQBlockPacingConfig(&qblockPacingConfig{ProbingRate: cfg.ProbingRate, NonProbingWait: cfg.NonProbingWait, MaxIntentBytes: cfg.MaxIntentBytes}, cfg.Manager); err != nil {
		return nil, err
	}

	runtime := &QBlockServerRuntime{inbound: true, config: cfg, domain: newQBlockEndpointDomain(realQBlockClock{}, cfg.ProbingRate, cfg.MaxPeers, cfg.MaxEndpointMembers)}
	if err := runtime.ValidateTransport(1); err != nil {
		return nil, err
	}
	return runtime, nil
}
func (r *QBlockServerRuntime) NewConn(session Session, cfg *Config, opts ...Option) (*Conn, error) {
	c := r.config
	private := qblockClientConfig{Manager: c.Manager, Clock: r.domain.clock, Pacing: &qblockPacingConfig{ProbingRate: c.ProbingRate, NonProbingWait: c.NonProbingWait, MaxIntentBytes: c.MaxIntentBytes}, Endpoint: r.domain, ScheduleMode: qblockScheduleAutomatic, MaxOwnedBytes: c.MaxOwnedBytes, MaxMIDEntries: c.MaxMIDEntries}
	if r.clientConfig != nil {
		copy := *r.clientConfig
		cfg.QBlock = &copy
		private = publicQBlockClientConfig(copy, r.domain)
	}
	opts = append(opts, withQBlockClient(private))
	if r.inbound {
		opts = append(opts, withQBlockServer(qblockServerConfig{Retention: c.Retention, MaxRecords: c.MaxRecords, MaxMetadataBytes: c.MaxMetadataBytes}))
	}
	cc := NewConnWithOpts(session, cfg, opts...)
	cc.qblockClient.serverOnly = r.clientConfig == nil
	if err := cc.InitializationError(); err != nil {
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

// ValidateTransport checks adapter reservations against the final plaintext datagram cap.
func (r *QBlockServerRuntime) ValidateTransport(datagram uint32) error {
	cfg := r.config
	if datagram == 0 {
		return qblock.ErrLimitExceeded
	}
	probe := &qblockClient{managerConfig: cfg.Manager, datagramLimit: datagram, maxMIDEntries: cfg.MaxMIDEntries, maxOwnedBytes: cfg.MaxOwnedBytes, pacingConfig: qblockPacingConfig{MaxIntentBytes: cfg.MaxIntentBytes}}
	if r.inbound {
		probe.server = &qblockServer{config: qblockServerConfig{MaxRecords: cfg.MaxRecords, MaxMetadataBytes: cfg.MaxMetadataBytes}}
	}
	return probe.initOwnedBudget()
}

// ValidateQBlockConfig validates outbound limits and transport reservations.
func ValidateQBlockConfig(cfg *Config) error {
	if cfg.QBlock == nil {
		return nil
	}
	c := *cfg.QBlock
	if err := c.Validate(); err != nil {
		return err
	}
	if cfg.BlockwiseSZX > 6 {
		return errInvalidQBlockClientConfig
	}
	if _, err := normalizeQBlockPacingConfig(&qblockPacingConfig{ProbingRate: c.ProbingRate, NonProbingWait: c.NonProbingWait, MaxIntentBytes: c.MaxIntentBytes}, c.Manager); err != nil {
		return err
	}
	d := min(uint32(cfg.MTU), cfg.MaxMessageSize)
	if d == 0 {
		return qblock.ErrLimitExceeded
	}
	probe := &qblockClient{managerConfig: c.Manager, datagramLimit: d, maxMIDEntries: c.MaxMIDEntries, maxOwnedBytes: c.MaxOwnedBytes, pacingConfig: qblockPacingConfig{MaxIntentBytes: c.MaxIntentBytes}}
	return probe.initOwnedBudget()
}
func publicQBlockClientConfig(c qblock.ClientConfig, domain *qblockEndpointDomain) qblockClientConfig {
	clock := qblockClock(realQBlockClock{})
	if domain != nil {
		clock = domain.clock
	}
	return qblockClientConfig{Manager: c.Manager, Clock: clock, Endpoint: domain, ScheduleMode: qblockScheduleAutomatic, Jitter: rand.Float64, MaxOwnedBytes: c.MaxOwnedBytes, MaxMIDEntries: c.MaxMIDEntries, Pacing: &qblockPacingConfig{ProbingRate: c.ProbingRate, NonProbingWait: c.NonProbingWait, MaxIntentBytes: c.MaxIntentBytes}}
}

// NewQBlockRuntime constructs one endpoint owner for optional outbound/inbound roles.
func NewQBlockRuntime(outbound *qblock.ClientConfig, inbound *qblock.ServerConfig) (*QBlockServerRuntime, error) {
	if outbound == nil {
		if inbound == nil {
			return nil, nil
		}
		return NewQBlockServerRuntime(*inbound)
	}
	if err := outbound.Validate(); err != nil {
		return nil, err
	}
	if inbound != nil {
		if err := inbound.Validate(); err != nil {
			return nil, err
		}
	}
	if inbound != nil && !outbound.MatchesServer(*inbound) {
		return nil, errInvalidQBlockClientConfig
	}
	c := DefaultRuntimeServerConfig(*outbound)
	if inbound != nil {
		c = *inbound
	}
	if _, err := normalizeQBlockPacingConfig(&qblockPacingConfig{ProbingRate: c.ProbingRate, NonProbingWait: c.NonProbingWait, MaxIntentBytes: c.MaxIntentBytes}, c.Manager); err != nil {
		return nil, err
	}
	copy := *outbound
	r := &QBlockServerRuntime{config: c, clientConfig: &copy, inbound: inbound != nil, domain: newQBlockEndpointDomain(realQBlockClock{}, c.ProbingRate, c.MaxPeers, c.MaxEndpointMembers)}
	return r, nil
}
func DefaultRuntimeServerConfig(c qblock.ClientConfig) qblock.ServerConfig {
	s := qblock.DefaultServerConfig()
	s.Manager = c.Manager
	s.ProbingRate = c.ProbingRate
	s.NonProbingWait = c.NonProbingWait
	s.MaxIntentBytes = c.MaxIntentBytes
	s.MaxOwnedBytes = c.MaxOwnedBytes
	s.MaxMIDEntries = c.MaxMIDEntries
	s.MaxPeers = c.MaxPeers
	s.MaxConnections = c.MaxConnections
	s.MaxEndpointMembers = c.MaxEndpointMembers
	return s
}
func (r *QBlockServerRuntime) MaxConnections() uint32 { return r.config.MaxConnections }
