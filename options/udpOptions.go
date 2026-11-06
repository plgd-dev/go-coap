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

// DTLSHandshakeTimeoutOpt configures server-side DTLS handshake timeout.
type DTLSHandshakeTimeoutOpt struct {
	timeout time.Duration
}

func (o DTLSHandshakeTimeoutOpt) DTLSServerApply(cfg *dtlsServer.Config) {
	cfg.HandshakeTimeout = o.timeout
}

// WithDTLSHandshakeTimeout sets timeout for DTLS handshake performed before OnNewConn callback.
// Set to 0 or a negative value to disable timeout and use server context cancellation only.
func WithDTLSHandshakeTimeout(timeout time.Duration) DTLSHandshakeTimeoutOpt {
	return DTLSHandshakeTimeoutOpt{timeout: timeout}
}
