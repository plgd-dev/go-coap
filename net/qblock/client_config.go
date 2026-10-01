package qblock

import (
	"errors"
	"time"
)

// Mode controls outbound selection using explicit session discovery.
type Mode uint8

const (
	PreferKnown Mode = iota
	Require
)

var (
	ErrCapabilityUnknown    = errors.New("q-block peer capability unknown")
	ErrPeerUnsupported      = errors.New("q-block peer unsupported")
	ErrUnsupportedOperation = errors.New("unsupported q-block operation")
)

// ClientConfig enables outbound Q selection. Start from DefaultClientConfig;
// zero is invalid. Byte limits are per connection; endpoint limits are per server.
// MaxOwnedBytes zero derives the runtime floor, NonProbingWait zero derives timing.
type ClientConfig struct {
	Mode                                                                         Mode
	Manager                                                                      ManagerConfig
	ProbingRate                                                                  uint64
	NonProbingWait                                                               time.Duration
	MaxIntentBytes, MaxOwnedBytes                                                uint64
	MaxMIDEntries, MaxProbeWaiters, MaxPeers, MaxConnections, MaxEndpointMembers uint32
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{Manager: DefaultManagerConfig(), ProbingRate: 1, MaxIntentBytes: 16 << 20, MaxMIDEntries: 65536, MaxProbeWaiters: 64, MaxPeers: 1024, MaxConnections: 1024, MaxEndpointMembers: 4096}
}
func (c ClientConfig) Validate() error {
	s := DefaultServerConfig()
	s.Manager = c.Manager
	s.ProbingRate = c.ProbingRate
	s.NonProbingWait = c.NonProbingWait
	s.MaxIntentBytes = c.MaxIntentBytes
	s.MaxOwnedBytes = c.MaxOwnedBytes
	s.MaxMIDEntries = c.MaxMIDEntries
	s.MaxPeers = c.MaxPeers
	s.MaxConnections = c.MaxConnections
	s.MaxEndpointMembers = c.MaxEndpointMembers
	if err := s.Validate(); err != nil {
		return err
	}
	if c.Mode > Require || c.MaxProbeWaiters == 0 || c.MaxProbeWaiters > 65536 {
		return errors.New("invalid q-block client limits")
	}
	return nil
}

// MatchesServer requires identical shared limits; role-specific fields differ.
func (c ClientConfig) MatchesServer(s ServerConfig) bool {
	return c.Manager == s.Manager && c.ProbingRate == s.ProbingRate && c.NonProbingWait == s.NonProbingWait && c.MaxIntentBytes == s.MaxIntentBytes && c.MaxOwnedBytes == s.MaxOwnedBytes && c.MaxMIDEntries == s.MaxMIDEntries && c.MaxPeers == s.MaxPeers && c.MaxConnections == s.MaxConnections && c.MaxEndpointMembers == s.MaxEndpointMembers
}
