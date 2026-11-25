package handshake

import (
	"container/list"
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

const (
	DefaultLimit   = 1024
	DefaultTimeout = 30 * time.Second
)

var ErrTooManyPending = errors.New("too many pending handshakes")

type Conn interface {
	net.Conn
	HandshakeContext(context.Context) error
}

type Pending struct {
	mu    sync.Mutex
	list  list.List
	limit uint32
}

type Attempt struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
	entry  *list.Element
}

func NewPending(limit uint32) *Pending {
	return &Pending{limit: limit}
}

// Admit waits for an evicted handshake to finish cleanup before admitting another.
func (p *Pending) Admit(ctx context.Context) *Attempt {
	for {
		p.mu.Lock()
		if ctx.Err() != nil {
			p.mu.Unlock()
			return nil
		}
		if p.limit == 0 || int64(p.list.Len()) < int64(p.limit) {
			handshakeCtx, cancel := context.WithCancelCause(ctx)
			a := &Attempt{ctx: handshakeCtx, cancel: cancel, done: make(chan struct{})}
			a.entry = p.list.PushBack(a)
			p.mu.Unlock()
			return a
		}
		oldest, ok := p.list.Front().Value.(*Attempt)
		if !ok {
			panic("invalid pending handshake")
		}
		oldest.cancel(ErrTooManyPending)
		p.mu.Unlock()
		select {
		case <-oldest.done:
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *Pending) remove(a *Attempt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cause := context.Cause(a.ctx)
	p.list.Remove(a.entry)
	close(a.done)
	a.cancel(nil)
	return cause
}

func (p *Pending) complete(a *Attempt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cause := context.Cause(a.ctx)
	if cause != nil {
		return cause
	}
	p.list.Remove(a.entry)
	close(a.done)
	a.cancel(nil)
	return nil
}

// Run performs the handshake and releases its slot after failure cleanup.
func (p *Pending) Run(a *Attempt, conn Conn, timeout time.Duration) error {
	ctx := a.ctx
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	err := conn.HandshakeContext(ctx)
	if err == nil {
		err = p.complete(a)
	}
	cancel()
	if err != nil {
		_ = conn.Close()
		if cause := p.remove(a); errors.Is(cause, ErrTooManyPending) {
			err = cause
		}
	}
	return err
}
