package options

import (
	"time"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
	udpServer "github.com/plgd-dev/go-coap/v3/udp/server"
)

// QBlockwiseMode selects datagram Q-Block behavior. Disabled remains the default.
type QBlockwiseMode = blockwise.QBlockwiseMode

// QBlockwiseConfig contains RFC 9177 Section 7.2 transfer parameters.
type QBlockwiseConfig = blockwise.QBlockwiseConfig

// QBlockwiseConfigOpt configures Q transfers independently of legacy blockwise.
type QBlockwiseConfigOpt struct {
	config QBlockwiseConfig
}

func (o QBlockwiseConfigOpt) UDPClientApply(cfg *udpClient.Config) {
	cfg.QBlockwiseConfig = o.config
}

func (o QBlockwiseConfigOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.QBlockwiseConfig = o.config
}

func (o QBlockwiseConfigOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.QBlockwiseConfig = o.config
}

// WithQBlockwiseConfig sets Q transfer parameters. Start from qblock.DefaultConfig
// for customization; a zero config uses defaults. MaxPayloads must match at both
// endpoints. Invalid parameters fail connection/server startup.
func WithQBlockwiseConfig(config QBlockwiseConfig) QBlockwiseConfigOpt {
	return QBlockwiseConfigOpt{config: config}
}

const (
	QBlockwiseDisabled = blockwise.QBlockwiseDisabled
	QBlockwiseAuto     = blockwise.QBlockwiseAuto
	QBlockwiseOnly     = blockwise.QBlockwiseOnly
)

// QBlockwiseOpt configures RFC 9177 selection on UDP and DTLS datagrams.
type QBlockwiseOpt struct {
	mode QBlockwiseMode
}

func (o QBlockwiseOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.QBlockwiseMode = o.mode
}

func (o QBlockwiseOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.QBlockwiseMode = o.mode
}

func (o QBlockwiseOpt) UDPClientApply(cfg *udpClient.Config) {
	cfg.QBlockwiseMode = o.mode
}

// WithQBlockwise selects Disabled, Auto, or Only for unicast datagram traffic.
// Auto and QOnly are experimental until the full RFC 9177 transfer lifecycle
// and interoperability suite are complete.
// Q Observe notifications use the registration token and recover missing payloads
// without re-registering. Proxy, multicast, and tokenless Q operations are rejected.
// Bodies are limited to 16 MiB and 65536 blocks.
// Q state is independently retained for RFC 9177 lifetimes rather than the
// legacy WithBlockwise timeout, with bounded per-peer and process-wide budgets.
func WithQBlockwise(mode QBlockwiseMode) QBlockwiseOpt {
	return QBlockwiseOpt{mode: mode}
}

// MaxPendingMessagesOpt configures in-flight duplicate suppression.
type MaxPendingMessagesOpt struct {
	maxPendingMessages uint32
}

func (o MaxPendingMessagesOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.MaxPendingMessages = o.maxPendingMessages
}

func (o MaxPendingMessagesOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.MaxPendingMessages = o.maxPendingMessages
}

func (o MaxPendingMessagesOpt) UDPClientApply(cfg *udpClient.Config) {
	cfg.MaxPendingMessages = o.maxPendingMessages
}

// WithMaxPendingMessages limits the number of in-flight Message IDs tracked per
// UDP/DTLS connection (default: 1024). Zero disables in-flight duplicate
// suppression without disabling the completed-response cache.
func WithMaxPendingMessages(maxPendingMessages uint32) MaxPendingMessagesOpt {
	return MaxPendingMessagesOpt{maxPendingMessages: maxPendingMessages}
}

// UDPServerWriteTimeoutOpt configures the UDP server socket write timeout.
type UDPServerWriteTimeoutOpt struct {
	timeout time.Duration
}

func (o UDPServerWriteTimeoutOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.WriteTimeout = o.timeout
}

// WithUDPServerWriteTimeout sets the timeout for a server UDP write, including
// time waiting for the shared socket. It must be positive; the default is 5s.
func WithUDPServerWriteTimeout(timeout time.Duration) UDPServerWriteTimeoutOpt {
	return UDPServerWriteTimeoutOpt{timeout: timeout}
}

// EndpointMessageIDOverflowMode selects the UDP server MID cache overflow policy.
type EndpointMessageIDOverflowMode = udpServer.EndpointMessageIDOverflowMode

// EndpointMessageIDCacheConfig configures the UDP server MID cache.
type EndpointMessageIDCacheConfig = udpServer.EndpointMessageIDCacheConfig

const (
	EndpointMessageIDFailOpen   = udpServer.EndpointMessageIDFailOpen
	EndpointMessageIDFailClosed = udpServer.EndpointMessageIDFailClosed
)

// DefaultEndpointMessageIDCacheConfig returns the default bounded, fail-open configuration.
func DefaultEndpointMessageIDCacheConfig() EndpointMessageIDCacheConfig {
	return udpServer.DefaultEndpointMessageIDCacheConfig()
}

// EndpointMessageIDCacheOpt configures the UDP server's per-peer MID cache.
type EndpointMessageIDCacheOpt struct {
	config EndpointMessageIDCacheConfig
}

func (o EndpointMessageIDCacheOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.EndpointMessageIDCache = o.config
}

// WithEndpointMessageIDCache configures UDP server peer MID retention.
// The default is 4096 entries with fail-open overflow. MaxEntries zero disables
// retention and requires fail-open, unless Unlimited is true. Unlimited
// requires MaxEntries zero and may cause unbounded memory growth.
// Active peers and peers whose last connection closed less than
// client.ExchangeLifetime ago are retained. Fail-open admits new peers with
// untracked sequences, potentially reusing MIDs; fail-closed rejects them.
// Spoofed UDP addresses can trigger the overflow policy or exhaust memory.
// MID wraparound, caller-assigned IDs and process restarts are not tracked.
func WithEndpointMessageIDCache(config EndpointMessageIDCacheConfig) EndpointMessageIDCacheOpt {
	return EndpointMessageIDCacheOpt{config: config}
}

// TransmissionOpt transmission options.
type TransmissionOpt struct {
	transmissionNStart                  uint32
	transmissionAcknowledgeTimeout      time.Duration
	transmissionAcknowledgeRandomFactor float64
	hasAcknowledgeRandomFactor          bool
	transmissionMaxRetransmit           uint32
}

func (o TransmissionOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.TransmissionNStart = o.transmissionNStart
	cfg.TransmissionAcknowledgeTimeout = o.transmissionAcknowledgeTimeout
	if o.hasAcknowledgeRandomFactor {
		cfg.TransmissionAcknowledgeRandomFactor = o.transmissionAcknowledgeRandomFactor
	}
	cfg.TransmissionMaxRetransmit = o.transmissionMaxRetransmit
}

func (o TransmissionOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.TransmissionNStart = o.transmissionNStart
	cfg.TransmissionAcknowledgeTimeout = o.transmissionAcknowledgeTimeout
	if o.hasAcknowledgeRandomFactor {
		cfg.TransmissionAcknowledgeRandomFactor = o.transmissionAcknowledgeRandomFactor
	}
	cfg.TransmissionMaxRetransmit = o.transmissionMaxRetransmit
}

func (o TransmissionOpt) UDPClientApply(cfg *udpClient.Config) {
	cfg.TransmissionNStart = o.transmissionNStart
	cfg.TransmissionAcknowledgeTimeout = o.transmissionAcknowledgeTimeout
	if o.hasAcknowledgeRandomFactor {
		cfg.TransmissionAcknowledgeRandomFactor = o.transmissionAcknowledgeRandomFactor
	}
	cfg.TransmissionMaxRetransmit = o.transmissionMaxRetransmit
}

// WithTransmission sets options for (re)transmission of Confirmable messages.
// The optional acknowledgeRandomFactor defaults to the value in the config (1.5).
func WithTransmission(transmissionNStart uint32,
	transmissionAcknowledgeTimeout time.Duration,
	transmissionMaxRetransmit uint32,
	acknowledgeRandomFactor ...float64,
) TransmissionOpt {
	opt := TransmissionOpt{
		transmissionNStart:             transmissionNStart,
		transmissionAcknowledgeTimeout: transmissionAcknowledgeTimeout,
		transmissionMaxRetransmit:      transmissionMaxRetransmit,
	}
	if len(acknowledgeRandomFactor) > 0 {
		opt.transmissionAcknowledgeRandomFactor = acknowledgeRandomFactor[0]
		opt.hasAcknowledgeRandomFactor = true
	}
	return opt
}

// MTUOpt transmission options.
type MTUOpt struct {
	mtu uint16
}

func (o MTUOpt) UDPServerApply(cfg *udpServer.Config) {
	cfg.MTU = o.mtu
}

func (o MTUOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.MTU = o.mtu
}

func (o MTUOpt) UDPClientApply(cfg *udpClient.Config) {
	cfg.MTU = o.mtu
}

// Setup MTU unit
func WithMTU(mtu uint16) MTUOpt {
	return MTUOpt{
		mtu: mtu,
	}
}
