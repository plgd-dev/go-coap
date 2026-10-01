package qblock

import (
	"errors"
	"time"
)

// ServerConfig enables NON Q-Block GET and POST/PUT handling on a UDP or DTLS server.
// Limits cover each connection; endpoint table limits cover the whole server.
// MaxOwnedBytes follows the adapter-owned copy/bookkeeping allowance model,
// excluding caller/handler allocations and preexisting message-pool capacity.
type ServerConfig struct {
	Manager                                                     ManagerConfig
	ProbingRate                                                 uint64
	NonProbingWait                                              time.Duration
	MaxIntentBytes                                              uint64
	Retention                                                   time.Duration
	MaxRecords                                                  uint32
	MaxMetadataBytes, MaxOwnedBytes                             uint64
	MaxMIDEntries, MaxPeers, MaxConnections, MaxEndpointMembers uint32
}

func DefaultServerConfig() ServerConfig {
	return ServerConfig{Manager: DefaultManagerConfig(), ProbingRate: 1, MaxIntentBytes: 16 << 20, Retention: 247 * time.Second, MaxRecords: 64, MaxMetadataBytes: 64 << 10, MaxMIDEntries: 65536, MaxPeers: 1024, MaxConnections: 1024, MaxEndpointMembers: 4096}
}
func (c ServerConfig) Validate() error {
	if err := c.Manager.Validate(); err != nil {
		return err
	}
	if c.ProbingRate == 0 || c.NonProbingWait < 0 || c.MaxIntentBytes == 0 || c.Retention < c.Manager.Transfer.Lifetime || c.MaxRecords == 0 || c.MaxMetadataBytes == 0 || c.MaxMIDEntries == 0 || c.MaxMIDEntries > 65536 || c.MaxPeers == 0 || c.MaxConnections == 0 || c.MaxEndpointMembers < c.MaxConnections || c.MaxPeers > 1<<20 || c.MaxConnections > 1<<20 || c.MaxEndpointMembers > 1<<20 {
		return errors.New("invalid q-block server limits")
	}
	return nil
}
