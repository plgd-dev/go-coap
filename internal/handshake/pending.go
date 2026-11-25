// Package handshake bounds concurrent server-side TLS and DTLS handshakes.
package handshake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// DefaultLimit is the default maximum number of pending handshakes.
	DefaultLimit = 1024
	// DefaultTimeout is the default server-side handshake timeout.
	DefaultTimeout = 30 * time.Second
)

// ErrTooManyPending indicates that a handshake was evicted to admit another.
var ErrTooManyPending = errors.New("too many pending handshakes")

// Conn is a connection that performs a context-aware handshake.
type Conn interface {
	net.Conn
	HandshakeContext(context.Context) error
}

// Pending tracks handshakes in admission order.
type Pending struct {
	mu          sync.Mutex
	first, last *Attempt
	count       uint64
	limit       uint64
	released    chan struct{}
}

// Attempt represents one admitted handshake and must be run exactly once.
type Attempt struct {
	pending    *Pending
	conn       Conn
	ctx        context.Context
	cancel     context.CancelCauseFunc
	closeOnce  sync.Once
	closed     chan struct{}
	prev, next *Attempt
	evicting   bool
}

// NewPending creates a tracker; a zero limit allows unlimited handshakes.
func NewPending(limit uint32) *Pending {
	return &Pending{limit: uint64(limit), released: make(chan struct{})}
}

// Admit registers conn or cancels and closes the oldest attempt. It waits for
// any slot to be released, and returns nil if ctx is canceled.
func (p *Pending) Admit(ctx context.Context, conn Conn) *Attempt {
	for {
		p.mu.Lock()
		if ctx.Err() != nil {
			p.mu.Unlock()
			return nil
		}
		if p.limit == 0 || p.count < p.limit {
			handshakeCtx, cancel := context.WithCancelCause(ctx)
			a := &Attempt{pending: p, conn: conn, ctx: handshakeCtx, cancel: cancel, closed: make(chan struct{}), prev: p.last}
			if p.last == nil {
				p.first = a
			} else {
				p.last.next = a
			}
			p.last = a
			p.count++
			p.mu.Unlock()
			return a
		}
		oldest := p.first
		if !oldest.evicting {
			oldest.evicting = true
			oldest.cancel(ErrTooManyPending)
			oldest.startClose()
		}
		released := p.released
		p.mu.Unlock()
		select {
		case <-released:
		case <-ctx.Done():
			return nil
		}
	}
}

func (p *Pending) unlink(a *Attempt) {
	if a.prev == nil {
		p.first = a.next
	} else {
		a.prev.next = a.next
	}
	if a.next == nil {
		p.last = a.prev
	} else {
		a.next.prev = a.prev
	}
	p.count--
	close(p.released)
	p.released = make(chan struct{})
	a.cancel(nil)
}

func (a *Attempt) startClose() {
	a.closeOnce.Do(func() {
		go func() {
			_ = a.conn.Close()
			close(a.closed)
		}()
	})
}

func (p *Pending) remove(a *Attempt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cause := context.Cause(a.ctx)
	p.unlink(a)
	return cause
}

func (p *Pending) complete(ctx context.Context, a *Attempt) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cause := context.Cause(ctx)
	if cause != nil {
		return cause
	}
	p.unlink(a)
	return nil
}

// Run performs the handshake and releases its slot after failure cleanup.
// It returns an error when called on a nil attempt.
func (a *Attempt) Run(timeout time.Duration) error {
	if a == nil {
		return errors.New("nil handshake attempt")
	}
	ctx := a.ctx
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	err := a.conn.HandshakeContext(ctx)
	if err == nil {
		err = a.pending.complete(ctx, a)
	}
	cancel()
	if err != nil {
		a.startClose()
		<-a.closed
		if cause := a.pending.remove(a); errors.Is(cause, ErrTooManyPending) {
			if !errors.Is(err, ErrTooManyPending) {
				// Do not wrap cancellation or close errors: server callbacks filter them.
				err = fmt.Errorf("%w: %s", cause, err.Error())
			} else {
				err = cause
			}
		}
	}
	return err
}
