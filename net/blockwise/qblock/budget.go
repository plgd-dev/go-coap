package qblock

import (
	"context"
	"sync/atomic"
)

// These process-wide ceilings also bound aggregate retention across server
// connections, not just the per-peer limits. They do not affect legacy bodies.
var (
	qRetainedBytes     atomic.Int64
	qRetainedTransfers atomic.Int64
)

func qReserve(counter *atomic.Int64, amount, limit int64) bool {
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

func qReserveBytes(n int64) bool { return qReserve(&qRetainedBytes, n, 256<<20) }
func qReserveTransfer() bool     { return qReserve(&qRetainedTransfers, 1, 8192) }

// CloseQBlockTransfers releases datagram body state when its owning connection
// closes. It is idempotent and prevents new Q state from being retained.
func (b *Engine[C]) CloseQBlockTransfers() {
	b.q1ReceiveMu.Lock()
	b.q1Closed = true
	for key, transfer := range b.q1Receivers {
		delete(b.q1Receivers, key)
		qRetainedBytes.Add(-transfer.bytes)
		qRetainedTransfers.Add(-1)
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
		qRetainedTransfers.Add(-1)
		delete(b.qObservations, key)
	}
	for key, transfer := range b.q2Senders {
		delete(b.q2Senders, key)
		qRetainedBytes.Add(-transfer.size)
		qRetainedTransfers.Add(-1)
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
