package server

import (
	"fmt"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options/config"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
)

// The HandlerFunc type is an adapter to allow the use of
// ordinary functions as COAP handlers.
type HandlerFunc = func(*responsewriter.ResponseWriter[*udpClient.Conn], *pool.Message)

type ErrorFunc = func(error)

// OnNewConnFunc is the callback for new connections.
type OnNewConnFunc = func(cc *udpClient.Conn)

type GetMIDFunc = func() int32

// DefaultMaxEndpointMessageIDs is the default number of retained peer sequences.
const DefaultMaxEndpointMessageIDs uint32 = 4096

// EndpointMessageIDOverflowMode controls admission when the MID cache is full.
type EndpointMessageIDOverflowMode uint8

const (
	// EndpointMessageIDFailOpen admits peers with an untracked random sequence.
	EndpointMessageIDFailOpen EndpointMessageIDOverflowMode = iota
	// EndpointMessageIDFailClosed rejects peers until a cache entry can be evicted.
	EndpointMessageIDFailClosed
)

// EndpointMessageIDCacheConfig controls retention of outgoing peer sequences.
type EndpointMessageIDCacheConfig struct {
	// MaxEntries limits retained peers. Zero disables retention when Unlimited is false.
	MaxEntries uint32
	// Unlimited removes the limit and requires MaxEntries to be zero.
	// It may allow unbounded memory growth.
	Unlimited bool
	// OverflowMode selects behavior when no entry can be evicted.
	// A zero limit without Unlimited requires EndpointMessageIDFailOpen.
	OverflowMode EndpointMessageIDOverflowMode
}

// DefaultEndpointMessageIDCacheConfig returns the default bounded, fail-open configuration.
func DefaultEndpointMessageIDCacheConfig() EndpointMessageIDCacheConfig {
	return EndpointMessageIDCacheConfig{
		MaxEntries:   DefaultMaxEndpointMessageIDs,
		OverflowMode: EndpointMessageIDFailOpen,
	}
}

var DefaultConfig = func() Config {
	opts := Config{
		Common: config.NewCommon[*udpClient.Conn](),
		CreateInactivityMonitor: func() udpClient.InactivityMonitor {
			timeout := time.Second * 16
			onInactive := func(cc *udpClient.Conn) {
				_ = cc.Close()
			}
			return udpClient.DeferInactivityWhileConfirmableMessagesPending(inactivity.New(timeout, onInactive))
		},
		OnNewConn: func(*udpClient.Conn) {
			// do nothing by default
		},
		RequestMonitor: func(*udpClient.Conn, *pool.Message) (bool, error) {
			return false, nil
		},
		TransmissionNStart:                  1,
		TransmissionAcknowledgeTimeout:      time.Second * 2,
		TransmissionAcknowledgeRandomFactor: 1.5,
		TransmissionMaxRetransmit:           4,
		GetMID:                              message.GetMID,
		MTU:                                 udpClient.DefaultMTU,
		MaxPendingMessages:                  udpClient.DefaultMaxPendingMessages,
		EndpointMessageIDCache:              DefaultEndpointMessageIDCacheConfig(),
	}
	opts.Handler = func(w *responsewriter.ResponseWriter[*udpClient.Conn], r *pool.Message) {
		if r.Type() == message.Confirmable && r.Code() >= 64 && r.Code() < 192 {
			w.Message().SetType(message.Reset)
			return
		}
		if err := w.SetResponse(codes.NotFound, message.TextPlain, nil); err != nil {
			opts.Errors(fmt.Errorf("udp server: cannot set response: %w", err))
		}
	}
	return opts
}()

type Config struct {
	config.Common[*udpClient.Conn]
	CreateInactivityMonitor udpClient.CreateInactivityMonitorFunc
	// GetMID contributes to the initial message ID for a newly tracked peer.
	// A random offset is added; subsequent connections reuse the peer's counter
	// while it remains in the server's endpoint cache.
	// It is called while the server's connection lock is held and must not block
	// or call back into the server. Its result does not determine the initial MID.
	GetMID                              GetMIDFunc
	Handler                             HandlerFunc
	OnNewConn                           OnNewConnFunc
	RequestMonitor                      udpClient.RequestMonitorFunc
	TransmissionNStart                  uint32
	TransmissionAcknowledgeTimeout      time.Duration
	TransmissionAcknowledgeRandomFactor float64
	TransmissionMaxRetransmit           uint32
	MTU                                 uint16
	// MaxPendingMessages limits in-flight duplicate suppression per connection.
	// Zero disables in-flight tracking.
	MaxPendingMessages uint32
	// EndpointMessageIDCache configures retained per-peer outgoing MID sequences.
	// Active peers and peers whose last connection closed less than
	// client.ExchangeLifetime ago cannot be evicted.
	// MID wraparound, caller-assigned IDs and process restarts are not tracked.
	EndpointMessageIDCache EndpointMessageIDCacheConfig
	// QBlockwiseMode is Disabled by default and supports plain UDP.
	QBlockwiseMode   blockwise.QBlockwiseMode
	QBlockwiseConfig blockwise.QBlockwiseConfig
}
