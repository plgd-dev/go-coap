package client

import (
	"io"
	"sync"
	"unsafe"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

func cloneQBlockBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	result := make([]byte, len(value))
	copy(result, value)
	return result
}

// readQBlockBody copies at most the limit and one overflow-detection byte.
// Failed reads never return a partial body for publication or transmission.
func readQBlockBody(reader io.Reader, limit uint32) ([]byte, error) {
	payload := make([]byte, int64(limit)+1)
	n, err := io.ReadFull(reader, payload)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	payload = payload[:n]
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) > uint64(limit) {
		return nil, qblock.ErrLimitExceeded
	}
	return payload, nil
}

func qblockClientSnapshotCapacity(options message.Options, token message.Token, tag []byte, maxPayloads uint32) (uint64, error) {
	capacity, err := qblockControlCapacity(options, maxPayloads)
	if err != nil {
		return 0, err
	}
	snapshot, err := qblockOptionBytes(options)
	if err != nil {
		return 0, err
	}
	for _, part := range []uint64{snapshot, uint64(len(token)), uint64(len(tag))} {
		var ok bool
		capacity, ok = qblockCheckedAdd(capacity, part)
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
	}
	return capacity, nil
}

// Reservations cover adapter copies plus an explicit conservative bookkeeping
// allowance. They do not measure runtime RSS or application/pool allocations.
type qblockOwnedBudget struct {
	mu                                         sync.Mutex
	limit, floor, used, clientCost, serverCost uint64
}

func (b *qblockOwnedBudget) acquire(cost uint64) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cost > b.limit-b.used {
		return nil, qblock.ErrLimitExceeded
	}
	b.used += cost
	var once sync.Once
	return func() { once.Do(func() { b.mu.Lock(); b.used -= cost; b.mu.Unlock() }) }, nil
}
func qblockMapAllowance(entries, key, value, referenced uint64) (uint64, error) {
	per, ok := qblockCheckedAdd(key, value)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	per, ok = qblockCheckedAdd(per, referenced+64)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	total, ok := qblockCheckedMul(entries, per)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	total, ok = qblockCheckedMul(total, 2)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	total, ok = qblockCheckedAdd(total, 256)
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	return total, nil
}
func (c *qblockClient) initOwnedBudget() error {
	m, n, t, p, l, i, d := uint64(c.managerConfig.Transfer.MaxBodySize), uint64(c.managerConfig.MaxTransfers), uint64(c.managerConfig.MaxTokens), uint64(c.managerConfig.Transfer.MaxPayloads), c.managerConfig.MaxRetainedBytes, c.pacingConfig.MaxIntentBytes, uint64(c.datagramLimit)
	r, h := uint64(0), uint64(0)
	if c.server != nil {
		r = uint64(c.server.config.MaxRecords)
		h = c.server.config.MaxMetadataBytes
	}
	var failure error
	add := func(values ...uint64) uint64 {
		var total uint64
		for _, v := range values {
			var ok bool
			total, ok = qblockCheckedAdd(total, v)
			if !ok {
				failure = qblock.ErrLimitExceeded
			}
		}
		return total
	}
	mul := func(a, b uint64) uint64 {
		v, ok := qblockCheckedMul(a, b)
		if !ok {
			failure = qblock.ErrLimitExceeded
		}
		return v
	}
	o := mul(d, uint64(unsafe.Sizeof(message.Option{}))+1)
	clientCost := add(mul(2, m+1), mul(4, o), uint64(unsafe.Sizeof(qblockExchange{})), uint64(unsafe.Sizeof(qblockTransfer{})), uint64(unsafe.Sizeof(qblockProbeGeneration{})), uint64(unsafe.Sizeof(qblockCapabilityProbe{})), 2048)
	serverCost := add(mul(4, m+1), mul(8, o), uint64(unsafe.Sizeof(qblockServerRecord{})), uint64(unsafe.Sizeof(qblockServerCONRecord{})), mul(4, d), 4096)
	blocks := min(uint64(1<<20), max(uint64(1), (m+15)/16))
	actions := mul(n, min(p, blocks)+3)
	executor := add(mul(2, l), mul(4, i), mul(8, o), mul(16, d), mul(mul(4, actions), uint64(unsafe.Sizeof(qblock.Action{}))+uint64(unsafe.Sizeof(qblock.Output{}))+uint64(unsafe.Sizeof(qblockCallback{}))), mul(mul(mul(16, n), p), 4))
	fixed := add(uint64(unsafe.Sizeof(qblockClient{})), uint64(unsafe.Sizeof(qblockServer{})), mul(n, uint64(unsafe.Sizeof(qblockQueuedCallback{}))*4), mul(mul(n, p), 32), 8192)
	// High-water inventories: manager, client exchange/transfer/work, server
	// records/indexes, retained tokens, MID ownership (including pending GET), repair/reply queues.
	inventories := [][4]uint64{
		{mul(n, 3), 16, 16, 0}, {mul(t, 2), 16, 16, 8},
		{mul(n, 8), 16, 16, 0}, {mul(t, 4), 16, 16, 8},
		{mul(r, 4), 16, 16, 0}, {add(mul(r, t), t), 16, 24, 8},
		{mul(uint64(c.maxMIDEntries), 2), 8, 16, 0}, {mul(mul(n, p), 2), 8, 24, 8},
		{add(n, mul(r, t), t), 8, 16, 8},
		// Explicit CON probes use the ordinary response cache for empty ACKs.
		// These outlive the active probe lease. Reserve their full 16-bit MID
		// namespace high-water storage, including keys and cache elements, in
		// the fixed floor; custom cache implementation allocations are caller-owned.
		{1 << 16, 16, 24, 128},
	}
	for _, inventory := range inventories {
		cost, err := qblockMapAllowance(inventory[0], inventory[1], inventory[2], inventory[3])
		if err != nil {
			failure = err
		}
		fixed = add(fixed, cost)
	}
	floor := add(l, i, mul(2, h), executor, fixed)
	limit := c.maxOwnedBytes
	if limit == 0 {
		limit = add(floor, mul(n, clientCost), mul(r, serverCost))
	}
	if failure != nil {
		return failure
	}
	if limit < floor || limit-floor < clientCost || (r > 0 && limit-floor < serverCost) {
		return qblock.ErrLimitExceeded
	}
	c.ownedBudget = &qblockOwnedBudget{limit: limit, floor: floor, used: floor, clientCost: clientCost, serverCost: serverCost}
	return nil
}

type qblockOwnedLease struct {
	mu      sync.Mutex
	refs    uint32
	release func()
}

func newQBlockOwnedLease(release func()) *qblockOwnedLease {
	return &qblockOwnedLease{refs: 1, release: release}
}
func (l *qblockOwnedLease) retain() func() {
	l.mu.Lock()
	l.refs++
	l.mu.Unlock()
	var once sync.Once
	return func() { once.Do(l.drop) }
}
func (l *qblockOwnedLease) drop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.refs--
	last := l.refs == 0
	l.mu.Unlock()
	if last {
		l.release()
	}
}
