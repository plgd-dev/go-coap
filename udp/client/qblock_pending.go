package client

import (
	"bytes"
	"math"
	"time"
	"unsafe"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
)

type qblockWorkID uint64
type qblockWorkKind uint8

const (
	qblockWorkBody qblockWorkKind = iota + 1
	qblockWorkGET
	qblockWorkControls
)

type qblockControlWork struct {
	Intent      qblock.ControlIntent
	ReplyToken  message.Token
	PacketIndex int
	ProbeKey    qblockProbeKey
}

// qblockPendingWork contains only copied metadata. Prepared bodies remain in
// the transfer manager; no message, callback, or transport closure is queued.
type qblockPendingWork struct {
	Kind           qblockWorkKind
	Server         bool
	Ungated        bool // Reserved for Q1 Continue responses, not Q2 requests.
	Operation      qblock.OperationKey
	TransferID     qblock.TransferID
	Generation     uint64
	Expires        time.Time
	ProbeKey       qblockProbeKey
	NonProbingWait time.Duration
	RequestCode    codes.Code
	RequestOptions message.Options
	RequestToken   message.Token
	Controls       []qblockControlWork
}

type qblockWorkSlot struct {
	reserve uint64
	extra   uint64
	seq     uint64
	pending *qblockPendingWork
}

// qblockWorkQueue is connection-owned and used under the connection mutex.
// Its byte budget covers newly retained dynamic backing storage, while its
// slot limit bounds the fixed map/record overhead.
type qblockWorkQueue struct {
	maxSlots uint32
	maxBytes uint64
	used     uint64
	nextID   qblockWorkID
	nextSeq  uint64
	slots    map[qblockWorkID]*qblockWorkSlot
}

func newQBlockWorkQueue(maxSlots uint32, maxBytes uint64) *qblockWorkQueue {
	return &qblockWorkQueue{maxSlots: maxSlots, maxBytes: maxBytes, slots: make(map[qblockWorkID]*qblockWorkSlot)}
}

func qblockCheckedAdd(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

func qblockCheckedMul(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

func qblockOptionBytes(options message.Options) (uint64, error) {
	bytes, ok := qblockCheckedMul(uint64(len(options)), uint64(unsafe.Sizeof(message.Option{})))
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	for _, option := range options {
		bytes, ok = qblockCheckedAdd(bytes, uint64(len(option.Value)))
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
	}
	return bytes, nil
}

// qblockControlCapacity reserves enough backing storage for copied options,
// a max-size request token, a Continue plus MaxPayloads-bounded report, and
// one report correlation retained across a replacement.
func qblockControlCapacity(options message.Options, maxPayloads uint32) (uint64, error) {
	if maxPayloads == 0 || maxPayloads > 1<<20 {
		return 0, qblock.ErrLimitExceeded
	}
	bytes, err := qblockOptionBytes(options)
	if err != nil {
		return 0, err
	}
	for _, part := range []uint64{
		3 * uint64(unsafe.Sizeof(qblockControlWork{})),
		4 * message.MaxTokenSize,
		2 * uint64(maxPayloads) * uint64(unsafe.Sizeof(uint32(0))),
	} {
		var ok bool
		bytes, ok = qblockCheckedAdd(bytes, part)
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
	}
	return bytes, nil
}

func (q *qblockWorkQueue) reserve(controlBytes uint64) (qblockWorkID, error) {
	if uint64(len(q.slots)) >= uint64(q.maxSlots) || controlBytes > q.maxBytes-q.used || q.nextID == math.MaxUint64 {
		return 0, qblock.ErrLimitExceeded
	}
	q.nextID++
	id := q.nextID
	q.slots[id] = &qblockWorkSlot{reserve: controlBytes}
	q.used += controlBytes
	return id, nil
}

func qblockWorkBytes(work qblockPendingWork) (uint64, error) {
	bytes, err := qblockOptionBytes(work.RequestOptions)
	if err != nil {
		return 0, err
	}
	parts := []uint64{uint64(len(work.RequestToken))}
	controlBytes, ok := qblockCheckedMul(uint64(len(work.Controls)), uint64(unsafe.Sizeof(qblockControlWork{})))
	if !ok {
		return 0, qblock.ErrLimitExceeded
	}
	parts = append(parts, controlBytes)
	if len(work.RequestToken) > message.MaxTokenSize {
		return 0, qblock.ErrLimitExceeded
	}
	for _, control := range work.Controls {
		if len(control.ReplyToken) > message.MaxTokenSize || len(control.Intent.Action.Payload) != 0 {
			return 0, qblock.ErrLimitExceeded
		}
		parts = append(parts, uint64(len(control.ReplyToken)))
		numberBytes, ok := qblockCheckedMul(uint64(len(control.Intent.Action.Numbers)), uint64(unsafe.Sizeof(uint32(0))))
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
		parts = append(parts, numberBytes)
	}
	for _, part := range parts {
		bytes, ok = qblockCheckedAdd(bytes, part)
		if !ok {
			return 0, qblock.ErrLimitExceeded
		}
	}
	return bytes, nil
}

func qblockCloneWork(work qblockPendingWork) qblockPendingWork {
	work.RequestToken = bytes.Clone(work.RequestToken)
	if len(work.RequestOptions) != 0 {
		options := make(message.Options, len(work.RequestOptions))
		for i, option := range work.RequestOptions {
			options[i] = message.Option{ID: option.ID, Value: bytes.Clone(option.Value)}
		}
		work.RequestOptions = options
	}
	if len(work.Controls) != 0 {
		controls := make([]qblockControlWork, len(work.Controls))
		copy(controls, work.Controls)
		for i := range controls {
			controls[i].ReplyToken = bytes.Clone(controls[i].ReplyToken)
			controls[i].Intent.Action.Numbers = append([]uint32(nil), controls[i].Intent.Action.Numbers...)
		}
		work.Controls = controls
	}
	return work
}

func (q *qblockWorkQueue) replace(id qblockWorkID, work qblockPendingWork, retainOrder bool) error {
	slot := q.slots[id]
	if slot == nil {
		return qblock.ErrUnknownTransfer
	}
	bytes, err := qblockWorkBytes(work)
	if err != nil {
		return err
	}
	extra := uint64(0)
	if bytes > slot.reserve {
		extra = bytes - slot.reserve
	}
	available := q.maxBytes - (q.used - slot.extra)
	if extra > available {
		return qblock.ErrLimitExceeded
	}
	if (!retainOrder || slot.pending == nil) && q.nextSeq == math.MaxUint64 {
		return qblock.ErrLimitExceeded
	}
	copy := qblockCloneWork(work)
	q.used = q.used - slot.extra + extra
	slot.extra = extra
	if !retainOrder || slot.pending == nil {
		q.nextSeq++
		slot.seq = q.nextSeq
	}
	slot.pending = &copy
	return nil
}

func (q *qblockWorkQueue) next(now time.Time, gate *qblockProbeGate) (qblockWorkID, qblockPendingWork, bool) {
	var expiredID, continueID, readyID qblockWorkID
	var expiredSeq, continueSeq, readySeq uint64
	gateReady := gate == nil || gate.ready(now)
	for id, slot := range q.slots {
		if slot.pending == nil {
			continue
		}
		work := slot.pending
		switch {
		case !work.Expires.IsZero() && !now.Before(work.Expires):
			if expiredID == 0 || slot.seq < expiredSeq {
				expiredID, expiredSeq = id, slot.seq
			}
		case work.Ungated:
			if continueID == 0 || slot.seq < continueSeq {
				continueID, continueSeq = id, slot.seq
			}
		case gateReady:
			if readyID == 0 || slot.seq < readySeq {
				readyID, readySeq = id, slot.seq
			}
		}
	}
	for _, id := range []qblockWorkID{expiredID, continueID, readyID} {
		if id != 0 {
			return id, qblockCloneWork(*q.slots[id].pending), true
		}
	}
	return 0, qblockPendingWork{}, false
}

func (q *qblockWorkQueue) nextDeadline(now time.Time, gate *qblockProbeGate) (time.Time, bool) {
	var earliest time.Time
	hasGated := false
	gateReady := gate == nil || gate.ready(now)
	for _, slot := range q.slots {
		if slot.pending == nil {
			continue
		}
		work := slot.pending
		if !work.Expires.IsZero() && (earliest.IsZero() || work.Expires.Before(earliest)) {
			earliest = work.Expires
		}
		if work.Ungated {
			return now, true
		}
		hasGated = true
	}
	if hasGated {
		if gateReady {
			return now, true
		}
		if gate != nil {
			if gateDeadline, ok := gate.nextDeadline(); ok && (earliest.IsZero() || gateDeadline.Before(earliest)) {
				earliest = gateDeadline
			}
		}
	}
	if earliest.IsZero() {
		return time.Time{}, false
	}
	if earliest.Before(now) {
		return now, true
	}
	return earliest, true
}

func (q *qblockWorkQueue) clearPending(id qblockWorkID) {
	slot := q.slots[id]
	if slot == nil {
		return
	}
	q.used -= slot.extra
	slot.extra = 0
	slot.pending = nil
}

func (q *qblockWorkQueue) release(id qblockWorkID) {
	slot := q.slots[id]
	if slot == nil {
		return
	}
	q.used -= slot.reserve + slot.extra
	delete(q.slots, id)
}
