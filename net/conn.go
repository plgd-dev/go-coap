package net

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	stdatomic "sync/atomic"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/internal/requestnotsent"
	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
	"github.com/plgd-dev/go-coap/v3/pkg/sync/atomic"
)

// Conn is a generic stream-oriented network connection that provides Read/Write with context.
//
// Multiple goroutines may invoke methods on a Conn simultaneously.
type Conn struct {
	connection       net.Conn
	closed           atomic.Bool
	handshakeContext func(ctx context.Context) error
	writeGate        chan struct{}
}

// NewConn creates connection over net.Conn.
func NewConn(c net.Conn) *Conn {
	connection := Conn{
		connection: c,
		writeGate:  make(chan struct{}, 1),
	}

	if v, ok := c.(interface {
		HandshakeContext(ctx context.Context) error
	}); ok {
		connection.handshakeContext = v.HandshakeContext
	}

	return &connection
}

// LocalAddr returns the local network address. The Addr returned is shared by all invocations of LocalAddr, so do not modify it.
func (c *Conn) LocalAddr() net.Addr {
	return c.connection.LocalAddr()
}

// NetConn returns the underlying connection that is wrapped by c. The Conn returned is shared by all invocations of Connection, so do not modify it.
func (c *Conn) NetConn() net.Conn {
	return c.connection
}

// RemoteAddr returns the remote network address. The Addr returned is shared by all invocations of RemoteAddr, so do not modify it.
func (c *Conn) RemoteAddr() net.Addr {
	return c.connection.RemoteAddr()
}

// Close closes the connection.
func (c *Conn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.connection.Close()
}

func (c *Conn) handshake(ctx context.Context) error {
	if c.handshakeContext != nil {
		err := c.handshakeContext(ctx)
		if err == nil {
			return nil
		}
		errC := c.Close()
		if errC == nil {
			return err
		}
		return fmt.Errorf("%v", []error{err, errC})
	}
	return nil
}

// WriteWithContext writes data with context. A failure after any bytes have
// been written closes the connection to prevent reuse of an incomplete frame.
func (c *Conn) WriteWithContext(ctx context.Context, data []byte) error {
	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	if err := c.handshake(ctx); err != nil {
		return err
	}
	select {
	case c.writeGate <- struct{}{}:
		defer func() { <-c.writeGate }()
	case <-ctx.Done():
		return &requestnotsent.Error{Err: writecontext.Error(ctx)}
	}
	if err := writecontext.Error(ctx); err != nil {
		return &requestnotsent.Error{Err: err}
	}
	if c.closed.Load() {
		return ErrConnectionIsClosed
	}
	if _, ok := c.connection.(*dtls.Conn); ok && ctx.Done() != nil {
		return c.writeDTLSWithContext(ctx, data)
	}
	written := 0
	started := false
	err := c.withWriteContext(ctx, func() error {
		var writeErr error
		written, started, writeErr = c.writeAll(ctx, data)
		return writeErr
	})
	if err != nil && (written > 0 || c.tlsWriteTimedOut(err, started)) {
		if closeErr := c.Close(); closeErr != nil {
			return errors.Join(err, fmt.Errorf("cannot close connection after partial write: %w", closeErr))
		}
	}
	return err
}

func (c *Conn) writeDTLSWithContext(ctx context.Context, data []byte) error {
	const (
		writePending int32 = iota
		writeStarted
		writeCanceled
	)
	type writeResult struct {
		written int
		err     error
	}
	result := make(chan writeResult, 1)
	data = bytes.Clone(data)
	var state stdatomic.Int32
	var written int
	err := c.withWriteContext(ctx, func() error {
		go func() {
			if !state.CompareAndSwap(writePending, writeStarted) {
				return
			}
			n, started, writeErr := c.writeAll(ctx, data)
			if !started && writeErr != nil && ctx.Err() != nil {
				writeErr = &requestnotsent.Error{Err: writecontext.Error(ctx)}
			}
			result <- writeResult{written: n, err: writeErr}
		}()
		select {
		case r := <-result:
			written = r.written
			return r.err
		case <-ctx.Done():
			if state.CompareAndSwap(writePending, writeCanceled) {
				return &requestnotsent.Error{Err: writecontext.Error(ctx)}
			}
			c.closeDTLSAsync()
			return writecontext.Error(ctx)
		}
	})
	if err != nil && written > 0 {
		c.closeDTLSAsync()
	}
	return err
}

func (c *Conn) closeDTLSAsync() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if err := c.connection.Close(); err != nil {
			log.Printf("cannot close DTLS connection after interrupted write: %v", err)
		}
	}()
}

func (c *Conn) tlsWriteTimedOut(err error, started bool) bool {
	if _, ok := c.connection.(*tls.Conn); !ok || !started {
		return false
	}
	var netErr net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout())
}

func (c *Conn) writeAll(ctx context.Context, data []byte) (written int, started bool, err error) {
	for written < len(data) {
		if err := ctx.Err(); err != nil {
			return written, started, err
		}
		if c.closed.Load() {
			return written, started, ErrConnectionIsClosed
		}
		started = true
		n, writeErr := c.connection.Write(data[written:])
		written += n
		if writeErr != nil {
			return written, started, writeErr
		}
		if n == 0 {
			return written, started, io.ErrNoProgress
		}
	}
	return written, started, nil
}

func (c *Conn) withWriteContext(ctx context.Context, write func() error) (err error) {
	if ctx.Done() == nil {
		return write()
	}
	deadline, _ := ctx.Deadline()
	if deadlineErr := c.connection.SetWriteDeadline(deadline); deadlineErr != nil {
		return errors.Join(writecontext.Error(ctx), fmt.Errorf("cannot set connection write deadline: %w", deadlineErr))
	}
	interrupted := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() {
		interrupted <- c.connection.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			if deadlineErr := <-interrupted; deadlineErr != nil && !c.closed.Load() {
				err = errors.Join(err, fmt.Errorf("cannot interrupt connection write: %w", deadlineErr))
			}
		}
		if deadlineErr := c.connection.SetWriteDeadline(time.Time{}); deadlineErr != nil && !c.closed.Load() {
			err = errors.Join(err, fmt.Errorf("cannot clear connection write deadline: %w", deadlineErr))
		}
	}()
	err = write()
	if err != nil {
		err = errors.Join(writecontext.Error(ctx), err)
	}
	return err
}

// ReadFullWithContext reads stream with context until whole buffer is satisfied.
func (c *Conn) ReadFullWithContext(ctx context.Context, buffer []byte) error {
	offset := 0
	for offset < len(buffer) {
		n, err := c.ReadWithContext(ctx, buffer[offset:])
		if err != nil {
			return fmt.Errorf("cannot read full from connection: %w", err)
		}
		offset += n
	}
	return nil
}

// ReadWithContext reads stream with context.
func (c *Conn) ReadWithContext(ctx context.Context, buffer []byte) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	default:
	}
	if c.closed.Load() {
		return -1, ErrConnectionIsClosed
	}
	if err := c.handshake(ctx); err != nil {
		return -1, err
	}
	return c.connection.Read(buffer)
}
