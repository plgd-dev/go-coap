package qblock

import (
	"errors"
	"slices"
	"time"
)

// Receiver assembles one pre-identified NON body. Its caller serializes
// events and validates operation ownership before delivering fragments.
type Receiver struct {
	kind            Kind
	cfg             TransferConfig
	body            *Body
	deferred        bool
	revision        uint64
	progress        uint64
	pendingContinue *ControlIntent
	pendingMissing  *ControlIntent

	expires time.Time
	due     time.Time

	contiguous   uint32
	continued    uint32
	retries      uint32
	lastMissing  []uint32
	haveFragment bool
	delivered    bool
	closed       bool
}

// NewReceiver validates and copies immutable body metadata without allocating
// storage for the announced body.
func NewReceiver(kind Kind, cfg TransferConfig, meta Metadata, now time.Time) (*Receiver, error) {
	if kind != Q1 && kind != Q2 {
		return nil, errors.New("invalid transfer kind")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	body, err := NewBody(meta, cfg.MaxBodySize)
	if err != nil {
		return nil, err
	}
	return &Receiver{
		kind:    kind,
		cfg:     cfg,
		body:    body,
		expires: now.Add(cfg.Lifetime),
	}, nil
}

// Receive validates and stores one copied fragment, then emits any newly due
// continuation, recovery, delivery, and release actions.
func (r *Receiver) Receive(meta Metadata, block Block, payload []byte, now time.Time) ([]Action, error) {
	if r.closed {
		return nil, ErrClosed
	}
	if !now.Before(r.expires) {
		return r.expire(), nil
	}

	duplicate, err := r.body.Add(meta, block, payload)
	if err != nil {
		return nil, err
	}
	if duplicate {
		if r.kind == Q1 {
			return []Action{{Kind: Duplicate, Block: block}}, nil
		}
		return nil, nil
	}
	r.progress++
	if r.deferred {
		r.pendingContinue = nil
		r.pendingMissing = nil
	}

	r.haveFragment = true
	r.retries = 0
	r.due = now.Add(r.cfg.NonReceiveTimeout)
	for r.contiguous < r.body.count {
		if _, ok := r.body.blocks[r.contiguous]; !ok {
			break
		}
		r.contiguous++
	}

	if r.body.Complete() {
		payload, err := r.body.Assemble()
		if err != nil {
			return nil, err
		}
		r.delivered = true
		r.due = time.Time{}
		r.lastMissing = nil
		r.pendingContinue = nil
		r.pendingMissing = nil
		actions := []Action{{Kind: Deliver, Payload: payload}}
		if r.kind == Q2 {
			r.close()
			actions = append(actions, Action{Kind: Release})
		}
		return actions, nil
	}

	actions := r.continuationActions()
	firstDue := r.contiguous - r.contiguous%r.cfg.MaxPayloads
	observedSet := block.Number - block.Number%r.cfg.MaxPayloads
	if observedSet > firstDue {
		missing := r.body.Missing(firstDue, setEnd(firstDue, r.body.count, r.cfg.MaxPayloads), int(r.cfg.MaxPayloads))
		if len(missing) > 0 && !slices.Equal(missing, r.lastMissing) && r.retries < r.cfg.NonMaxRetransmit {
			if r.deferred {
				r.queueControl(Action{Kind: RequestMissing, Numbers: missing})
				r.due = time.Time{}
			} else {
				r.lastMissing = slices.Clone(missing)
				r.retries++
				r.due = now.Add(r.cfg.retryDelay(r.retries))
				actions = append(actions, Action{Kind: RequestMissing, Numbers: missing})
			}
		}
	}
	return actions, nil
}

func (r *Receiver) continuationActions() []Action {
	latest := r.contiguous - r.contiguous%r.cfg.MaxPayloads
	if latest <= r.continued || latest >= r.body.count {
		return nil
	}
	if r.deferred {
		r.queueControl(Action{Kind: SendContinue, Through: latest - 1})
		r.due = time.Time{}
		return nil
	}
	r.continued = latest
	return []Action{{Kind: SendContinue, Through: latest - 1}}
}

// Tick advances recovery or absolute expiry at the supplied event time.
func (r *Receiver) Tick(now time.Time) []Action {
	if r.closed {
		return nil
	}
	if !now.Before(r.expires) {
		return r.expire()
	}
	if r.delivered || !r.haveFragment || r.due.IsZero() || now.Before(r.due) {
		return nil
	}
	if r.retries >= r.cfg.NonMaxRetransmit {
		return r.finish(ErrRetriesExhausted)
	}

	first := r.contiguous - r.contiguous%r.cfg.MaxPayloads
	missing := r.body.Missing(first, setEnd(first, r.body.count, r.cfg.MaxPayloads), int(r.cfg.MaxPayloads))
	if len(missing) == 0 {
		return nil
	}
	if r.deferred {
		r.queueControl(Action{Kind: RequestMissing, Numbers: missing})
		r.due = time.Time{}
		return nil
	}
	r.lastMissing = slices.Clone(missing)
	r.retries++
	r.due = now.Add(r.cfg.retryDelay(r.retries))
	return []Action{{Kind: RequestMissing, Numbers: missing}}
}

// Cancel terminates the receiver and releases all retained body state.
func (r *Receiver) Cancel(err error) []Action {
	if r.closed {
		return nil
	}
	if err == nil {
		err = ErrCanceled
	}
	return r.finish(err)
}

func (r *Receiver) expire() []Action {
	if r.delivered {
		r.close()
		return []Action{{Kind: Release}}
	}
	return r.finish(ErrExpired)
}

func (r *Receiver) finish(err error) []Action {
	wasDelivered := r.delivered
	r.close()
	if wasDelivered {
		return []Action{{Kind: Release}}
	}
	return []Action{{Kind: Complete, Err: err}, {Kind: Release}}
}

func (r *Receiver) close() {
	r.closed = true
	r.body = nil
	r.due = time.Time{}
	r.lastMissing = nil
	r.pendingContinue = nil
	r.pendingMissing = nil
}

// NextDeadline returns the earliest recovery or absolute-lifetime deadline.
func (r *Receiver) NextDeadline() (time.Time, bool) {
	if r.closed {
		return time.Time{}, false
	}
	if !r.due.IsZero() && r.due.Before(r.expires) {
		return r.due, true
	}
	return r.expires, true
}
