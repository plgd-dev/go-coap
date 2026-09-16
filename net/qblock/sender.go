package qblock

import (
	"bytes"
	"errors"
	"slices"
	"time"
)

// Sender drives one pre-identified NON body. Its caller serializes events,
// validates control ownership and executes each burst before the next event.
// A failed write must Cancel the transfer; unsent actions must be discarded.
type Sender struct {
	kind                                    Kind
	cfg                                     TransferConfig
	meta                                    Metadata
	payload                                 []byte
	count, next, through                    uint32
	repairs                                 []uint32
	delay                                   time.Duration
	due, expires                            time.Time
	started, closed, notified, repairActive bool
}

// NewSender copies the body and samples its fixed send delay from jitter [0,1].
func NewSender(kind Kind, cfg TransferConfig, meta Metadata, payload []byte, now time.Time, jitter float64) (*Sender, error) {
	if kind != Q1 && kind != Q2 {
		return nil, errors.New("invalid transfer kind")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	delay, err := cfg.sendDelay(jitter)
	if err != nil {
		return nil, err
	}
	if uint64(len(payload)) != uint64(meta.Size) {
		return nil, errors.New("payload size differs from metadata")
	}
	body, err := NewBody(meta, cfg.MaxBodySize)
	if err != nil {
		return nil, err
	}
	return &Sender{kind: kind, cfg: cfg, meta: body.meta, payload: bytes.Clone(payload), count: body.count, delay: delay, expires: now.Add(cfg.Lifetime)}, nil
}

// Start emits the first payload set exactly once.
func (s *Sender) Start(now time.Time) ([]Action, error) {
	if s.closed {
		return nil, ErrClosed
	}
	if s.started {
		return nil, ErrAlreadyStarted
	}
	if !now.Before(s.expires) {
		return s.Cancel(ErrExpired), nil
	}
	s.started = true
	return s.sendInitial(now), nil
}

func (s *Sender) sendBlock(number uint32) Action {
	size := uint64(16) << s.meta.SZX
	offset := uint64(number) * size
	end := min(offset+size, uint64(len(s.payload)))
	return Action{Kind: SendBlock, Block: Block{Number: number, More: number+1 < s.count, SZX: s.meta.SZX}, Payload: bytes.Clone(s.payload[offset:end])}
}

func (s *Sender) sendInitial(now time.Time) []Action {
	end := setEnd(s.next, s.count, s.cfg.MaxPayloads)
	actions := make([]Action, 0, end-s.next+1)
	for n := s.next; n < end; n++ {
		actions = append(actions, s.sendBlock(n))
	}
	s.next = end
	s.through = end - 1
	s.due = time.Time{}
	if end < s.count {
		s.due = now.Add(s.delay)
	} else if s.kind == Q2 && !s.notified {
		s.notified = true
		actions = append(actions, Action{Kind: Complete})
	}
	return actions
}

// Continue accepts only the current initial set's highest block number.
// The manager must validate the associated token before invoking this method.
func (s *Sender) Continue(through uint32, now time.Time) ([]Action, error) {
	if s.closed {
		return nil, ErrClosed
	}
	if !s.started {
		return nil, errors.New("sender has not started")
	}
	if !now.Before(s.expires) {
		return s.Cancel(ErrExpired), nil
	}
	if s.repairActive || through != s.through || s.next >= s.count {
		return nil, nil
	}
	return s.sendInitial(now), nil
}

// Repair queues an ascending unique report of blocks already initially sent.
// Invalid reports leave the transfer unchanged. Queued repairs are bounded.
func (s *Sender) Repair(numbers []uint32, now time.Time) ([]Action, error) {
	if s.closed {
		return nil, ErrClosed
	}
	if !s.started {
		return nil, errors.New("sender has not started")
	}
	if len(numbers) == 0 || uint64(len(numbers)) > uint64(s.cfg.MaxPayloads) {
		return nil, ErrInvalidRepair
	}
	var previous uint32
	for i, n := range numbers {
		if n >= s.next || (i > 0 && n <= previous) {
			return nil, ErrInvalidRepair
		}
		previous = n
	}
	merged := append(slices.Clone(s.repairs), numbers...)
	slices.Sort(merged)
	merged = slices.Compact(merged)
	if uint64(len(merged)) > uint64(s.cfg.MaxPayloads) {
		return nil, ErrInvalidRepair
	}
	if !now.Before(s.expires) {
		return s.Cancel(ErrExpired), nil
	}
	s.repairs = merged
	if s.repairActive {
		return nil, nil
	}
	s.repairActive = true
	return s.sendRepairs(now), nil
}

func (s *Sender) sendRepairs(now time.Time) []Action {
	end := setEnd(s.repairs[0], s.count, s.cfg.MaxPayloads)
	var actions []Action
	consumed := 0
	for _, n := range s.repairs {
		if n >= end {
			break
		}
		actions = append(actions, s.sendBlock(n))
		consumed++
	}
	s.repairs = s.repairs[consumed:]
	s.due = now.Add(s.delay)
	return actions
}

// Tick advances at most one payload set, even when the caller is late.
func (s *Sender) Tick(now time.Time) []Action {
	if s.closed {
		return nil
	}
	if !now.Before(s.expires) {
		return s.Cancel(ErrExpired)
	}
	if !s.started || s.due.IsZero() || now.Before(s.due) {
		return nil
	}
	if len(s.repairs) > 0 {
		return s.sendRepairs(now)
	}
	s.repairActive = false
	s.due = time.Time{}
	if s.next < s.count {
		return s.sendInitial(now)
	}
	return nil
}

// Finish records a terminal outcome and releases the retained body.
func (s *Sender) Finish(err error) []Action {
	if s.closed {
		return nil
	}
	s.closed = true
	s.payload = nil
	s.repairs = nil
	s.meta.Identity = nil
	s.due = time.Time{}
	var actions []Action
	if !s.notified {
		s.notified = true
		actions = append(actions, Action{Kind: Complete, Err: err})
	}
	return append(actions, Action{Kind: Release})
}

// Cancel terminates a transfer after cancellation, expiry, or a failed send.
func (s *Sender) Cancel(err error) []Action {
	if err == nil {
		err = ErrCanceled
	}
	return s.Finish(err)
}

// NextDeadline returns the next pacing or absolute-lifetime deadline.
func (s *Sender) NextDeadline() (time.Time, bool) {
	if s.closed {
		return time.Time{}, false
	}
	if !s.due.IsZero() && s.due.Before(s.expires) {
		return s.due, true
	}
	return s.expires, true
}
