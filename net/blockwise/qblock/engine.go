// Package qblock implements RFC 9177 datagram transfers independently of the
// legacy RFC 7959 blockwise engine.
package qblock

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise/internal/blocks"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
)

// Client provides pooled messages to the Q-Block engine.
type Client = blocks.Client

// SZX represents a block size exponent shared with RFC 7959.
type SZX = blocks.SZX

const (
	SZX16   = blocks.SZX16
	SZX32   = blocks.SZX32
	SZX64   = blocks.SZX64
	SZX128  = blocks.SZX128
	SZX256  = blocks.SZX256
	SZX512  = blocks.SZX512
	SZX1024 = blocks.SZX1024
	SZXBERT = blocks.SZXBERT
)

const (
	maxBlockValue           = 0xffffff
	moreBlocksFollowingMask = 0x8
	szxMask                 = 0x7
)

// Shared wire errors retain their identity across both blockwise engines.
var (
	ErrBlockNumberExceedLimit = blocks.ErrBlockNumberExceedLimit
	ErrBlockInvalidSize       = blocks.ErrBlockInvalidSize
	ErrInvalidOptionBlock2    = blocks.ErrInvalidOptionBlock2
	ErrInvalidOptionBlock1    = blocks.ErrInvalidOptionBlock1
	ErrInvalidSZX             = blocks.ErrInvalidSZX
	ErrRequestEntityTooLarge  = blocks.ErrRequestEntityTooLarge
)

var errReceivingMessagesCacheFull = errors.New("receiving messages cache is full")

// Engine owns Q-Block transfer and observation state for one connection.
// The transport owns its deadline worker and response-token routing.
type Engine[C Client] struct {
	receivingMessagesCacheMaxEntries atomic.Int64
	qRetainedBytes                   atomic.Int64
	qRetainedTransfers               atomic.Int64

	config                Config
	cc                    C
	errors                func(error)
	maxReceiveBodySize    int64
	q1ReceiveMu           sync.Mutex
	q1Receivers           map[string]*q1ReceiveTransfer
	q1ReceiveBytes        int64
	q1Closed              bool
	q2SendMu              sync.Mutex
	q2Senders             map[string]*q2SendTransfer
	qObservations         map[string]*qObservation
	qCanceledObservations map[string]time.Time
	q2SendBytes           int64
	q2Closed              bool
}

// New creates an engine. reportError must be non-nil when using server request
// handling or scheduled operations that report protocol errors through it.
func New[C Client](cc C, reportError func(error)) *Engine[C] {
	b := &Engine[C]{
		config:      DefaultConfig(),
		cc:          cc,
		errors:      reportError,
		q1Receivers: make(map[string]*q1ReceiveTransfer),
		q2Senders:   make(map[string]*q2SendTransfer),
	}
	b.receivingMessagesCacheMaxEntries.Store(4096)
	return b
}

// SetConfig configures RFC 9177 parameters before any transfer is started.
// Like the body/cache limit setters, it must not run concurrently with transfers.
func (b *Engine[C]) SetConfig(config Config) error {
	resolved, err := config.Resolve()
	if err != nil {
		return err
	}
	b.config = resolved
	return nil
}

// SetMaxReceiveBodySize limits incoming Q1 bodies; zero uses the Q default.
func (b *Engine[C]) SetMaxReceiveBodySize(size int64) {
	b.maxReceiveBodySize = max(size, 0)
}

// SetReceivingMessagesCacheMaxEntries limits concurrent incoming Q1 assemblies.
// Zero removes the configured limit, but not the engine's hard resource ceiling.
func (b *Engine[C]) SetReceivingMessagesCacheMaxEntries(maxEntries int) {
	b.receivingMessagesCacheMaxEntries.Store(int64(max(maxEntries, 0)))
}

// CheckExpirations releases expired retained bodies and completed-request state.
func (b *Engine[C]) CheckExpirations(now time.Time) {
	b.q1ReceiveMu.Lock()
	for key, transfer := range b.q1Receivers {
		if !transfer.expires.After(now) {
			delete(b.q1Receivers, key)
			b.q1ReceiveBytes -= transfer.bytes
			b.qReleaseBytes(transfer.bytes)
			b.qReleaseTransfer()
			if transfer.response != nil {
				b.cc.ReleaseMessage(transfer.response)
			}
		}
	}
	b.q1ReceiveMu.Unlock()
	b.q2SendMu.Lock()
	for key, transfer := range b.q2Senders {
		if !transfer.expires.After(now) {
			delete(b.q2Senders, key)
			b.q2SendBytes -= transfer.size
			b.qReleaseBytes(transfer.size)
			b.qReleaseTransfer()
			b.cc.ReleaseMessage(transfer.message)
		}
	}
	b.q2SendMu.Unlock()
}

func (b *Engine[C]) cloneMessage(r *pool.Message) *pool.Message {
	return blocks.CloneMessage(b.cc, r)
}

func isRequestCode(code codes.Code) bool {
	return blocks.IsRequestCode(code)
}

func (b *Engine[C]) sendServiceUnavailable(w *responsewriter.ResponseWriter[C], token message.Token) {
	blocks.SendServiceUnavailable(b.cc, w, token)
}

func (b *Engine[C]) sendEntityTooLarge(w *responsewriter.ResponseWriter[C], token message.Token) {
	limit := b.maxReceiveBodySize
	if limit <= 0 {
		limit = q1DefaultMaxBodySize
	}
	blocks.SendEntityTooLarge(b.cc, w, token, limit)
}
