package qblock

import (
	"context"
	"sync/atomic"
)

// Each engine shares one quota across Q1 receivers, Q2 senders, client bodies,
// and observations: at most 1/8 of process bytes and 1/64 of process slots.
// The byte quota still permits a complete 16 MiB representation.
const (
	qProcessMaxBytes     = 256 << 20
	qProcessMaxTransfers = 8192
	qEngineMaxBytes      = 32 << 20
	qEngineMaxTransfers  = 128
)

// These process-wide ceilings also bound aggregate retention across server
// connections, not just the per-peer limits. They do not affect legacy bodies.
var (
	qRetainedBytes     atomic.Int64
	qRetainedTransfers atomic.Int64
)

func qReserve(counter *atomic.Int64, amount, limit int64) bool {
	if amount < 0 {
		return false
	}
	for {
		current := counter.Load()
		if amount > limit-current {
			return false
		}
		if counter.CompareAndSwap(current, current+amount) {
			return true
		}
	}
}

func (b *Engine[C]) qReserveBytes(n int64) bool {
	if !qReserve(&b.qRetainedBytes, n, qEngineMaxBytes) {
		return false
	}
	if !qReserve(&qRetainedBytes, n, qProcessMaxBytes) {
		b.qRetainedBytes.Add(-n)
		return false
	}
	return true
}

func (b *Engine[C]) qReserveTransfer() bool {
	if !qReserve(&b.qRetainedTransfers, 1, qEngineMaxTransfers) {
		return false
	}
	if !qReserve(&qRetainedTransfers, 1, qProcessMaxTransfers) {
		b.qRetainedTransfers.Add(-1)
		return false
	}
	return true
}

func (b *Engine[C]) qReleaseBytes(n int64) {
	b.qRetainedBytes.Add(-n)
	qRetainedBytes.Add(-n)
}

func (b *Engine[C]) qReleaseTransfer() {
	b.qRetainedTransfers.Add(-1)
	qRetainedTransfers.Add(-1)
}

// CloseQBlockTransfers releases datagram body state when its owning connection
// closes. It is idempotent and prevents new Q state from being retained.
func (b *Engine[C]) CloseQBlockTransfers() {
	b.q1ReceiveMu.Lock()
	b.q1Closed = true
	for key, transfer := range b.q1Receivers {
		delete(b.q1Receivers, key)
		b.qReleaseBytes(transfer.bytes)
		b.qReleaseTransfer()
		if transfer.response != nil {
			b.cc.ReleaseMessage(transfer.response)
		}
	}
	b.q1ReceiveBytes = 0
	b.q1ReceiveMu.Unlock()
	b.q2SendMu.Lock()
	b.q2Closed = true
	clear(b.qCanceledObservations)
	for key, observation := range b.qObservations {
		b.cc.ReleaseMessage(observation.request)
		b.qReleaseTransfer()
		delete(b.qObservations, key)
	}
	for key, transfer := range b.q2Senders {
		delete(b.q2Senders, key)
		b.qReleaseBytes(transfer.size)
		b.qReleaseTransfer()
		b.cc.ReleaseMessage(transfer.message)
	}
	b.q2SendBytes = 0
	b.q2SendMu.Unlock()
}

func qClosedError(closed bool) error {
	if closed {
		return context.Canceled
	}
	return nil
}
