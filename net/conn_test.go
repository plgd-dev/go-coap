package net

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNameCancelled = "cancelled"

func TestConnWriteWithContext(t *testing.T) {
	ctxCanceled, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	helloWorld := make([]byte, 1024*1024*256)

	type args struct {
		ctx  context.Context
		data []byte
	}

	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "valid",
			args: args{
				ctx:  context.Background(),
				data: helloWorld,
			},
		},
		{
			name: testNameCancelled,
			args: args{
				ctx:  ctxCanceled,
				data: helloWorld,
			},
			wantErr: true,
		},
	}

	listener, err := NewTCPListener("tcp", "127.0.0.1:")
	require.NoError(t, err)
	defer func() {
		err := listener.Close()
		require.NoError(t, err)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			conn, err := listener.AcceptWithContext(ctx)
			if err != nil {
				return
			}
			c := NewConn(conn)
			b := make([]byte, len(helloWorld))
			_ = c.ReadFullWithContext(ctx, b)
			err = c.Close()
			assert.NoError(t, err)
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dialer := net.Dialer{}
			tcpConn, err := dialer.DialContext(context.Background(), "tcp", listener.Addr().String())
			require.NoError(t, err)
			c := NewConn(tcpConn)
			defer func() {
				errC := c.Close()
				require.NoError(t, errC)
			}()

			c.LocalAddr()
			c.RemoteAddr()

			err = c.WriteWithContext(tt.args.ctx, tt.args.data)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestConnWriteWithContextInterruptsBlockedWrite(t *testing.T) {
	clientConn, peerConn := net.Pipe()
	defer clientConn.Close()
	defer peerConn.Close()

	conn := NewConn(clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := conn.WriteWithContext(ctx, make([]byte, 1024))
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout())

	read := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, readErr := io.ReadFull(peerConn, buf)
		read <- readErr
	}()
	require.NoError(t, conn.WriteWithContext(context.Background(), []byte{1}))
	require.NoError(t, <-read)
}

type scriptedWriteResult struct {
	n   int
	err error
}

type scriptedWriteConn struct {
	net.Conn
	results  []scriptedWriteResult
	writes   int
	closeErr error
	onWrite  func()
}

func (c *scriptedWriteConn) Write(data []byte) (int, error) {
	c.writes++
	if c.onWrite != nil {
		c.onWrite()
	}
	if len(c.results) == 0 {
		return len(data), nil
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result.n, result.err
}

func (c *scriptedWriteConn) Close() error {
	err := c.Conn.Close()
	return errors.Join(err, c.closeErr)
}

type limitedWriteConn struct {
	net.Conn
	limit int
}

type observedContext struct {
	context.Context
	waiting chan struct{}
}

func (c observedContext) Done() <-chan struct{} {
	close(c.waiting)
	return c.Context.Done()
}

func (c *limitedWriteConn) Write(data []byte) (int, error) {
	if len(data) > c.limit {
		data = data[:c.limit]
	}
	return c.Conn.Write(data)
}

func TestConnWriteWithContextPartialFailure(t *testing.T) {
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	for _, tt := range []struct {
		name       string
		results    []scriptedWriteResult
		err        error
		closeErr   error
		wantClosed bool
		wantWrites int
	}{
		{
			name:       "failure after earlier partial write",
			results:    []scriptedWriteResult{{n: 2}, {err: writeErr}},
			err:        writeErr,
			wantClosed: true,
			wantWrites: 2,
		},
		{
			name:       "partial write and error together",
			results:    []scriptedWriteResult{{n: 2, err: writeErr}},
			err:        writeErr,
			wantClosed: true,
			wantWrites: 1,
		},
		{
			name:       "all bytes and error together",
			results:    []scriptedWriteResult{{n: 4, err: writeErr}},
			err:        writeErr,
			wantClosed: true,
			wantWrites: 1,
		},
		{
			name:       "close error is reported with write error",
			results:    []scriptedWriteResult{{n: 2, err: writeErr}},
			err:        writeErr,
			closeErr:   closeErr,
			wantClosed: true,
			wantWrites: 1,
		},
		{
			name:       "failure before writing",
			results:    []scriptedWriteResult{{err: writeErr}},
			err:        writeErr,
			wantWrites: 1,
		},
		{
			name:       "zero progress before writing",
			results:    []scriptedWriteResult{{}},
			err:        io.ErrNoProgress,
			wantWrites: 1,
		},
		{
			name:       "zero progress after partial write",
			results:    []scriptedWriteResult{{n: 2}, {}},
			err:        io.ErrNoProgress,
			wantClosed: true,
			wantWrites: 2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer peer.Close()
			underlying := &scriptedWriteConn{Conn: client, results: tt.results, closeErr: tt.closeErr}
			conn := NewConn(underlying)
			defer conn.Close()

			err := conn.WriteWithContext(context.Background(), []byte("test"))
			require.ErrorIs(t, err, tt.err)
			if tt.closeErr != nil {
				require.ErrorIs(t, err, tt.closeErr)
			}
			require.Equal(t, tt.wantWrites, underlying.writes)

			err = conn.WriteWithContext(context.Background(), []byte("next"))
			if tt.wantClosed {
				require.ErrorIs(t, err, ErrConnectionIsClosed)
				require.Equal(t, tt.wantWrites, underlying.writes)
				_, err = peer.Read(make([]byte, 1))
				require.ErrorIs(t, err, io.EOF)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantWrites+1, underlying.writes)
			}
		})
	}
}

func TestConnWriteWithContextTimeoutAfterPartialWrite(t *testing.T) {
	for _, tt := range []struct {
		name        string
		limitWrites bool
	}{
		{name: "partial progress and timeout in same write"},
		{name: "timeout after earlier write", limitWrites: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, peer := net.Pipe()
			defer peer.Close()
			underlying := client
			if tt.limitWrites {
				underlying = &limitedWriteConn{Conn: client, limit: 2}
			}
			conn := NewConn(underlying)
			defer conn.Close()

			read := make(chan error, 1)
			go func() {
				buf := make([]byte, 2)
				_, err := io.ReadFull(peer, buf)
				read <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := conn.WriteWithContext(ctx, []byte("test"))
			require.NoError(t, <-read)
			require.Error(t, err)
			var netErr net.Error
			require.ErrorAs(t, err, &netErr)
			require.True(t, netErr.Timeout())
			require.ErrorIs(t, conn.WriteWithContext(context.Background(), []byte("next")), ErrConnectionIsClosed)
			_, err = peer.Read(make([]byte, 1))
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestConnWriteWithContextSuccessfulPartialWrites(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	underlying := &scriptedWriteConn{Conn: client, results: []scriptedWriteResult{{n: 2}, {n: 2}}}
	conn := NewConn(underlying)
	defer conn.Close()

	require.NoError(t, conn.WriteWithContext(context.Background(), []byte("test")))
	require.Equal(t, 2, underlying.writes)
	require.NoError(t, conn.WriteWithContext(context.Background(), []byte("next")))
	require.Equal(t, 3, underlying.writes)
}

func TestConnWriteWithContextCancellationAfterPartialWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	underlying := &scriptedWriteConn{
		Conn:    client,
		results: []scriptedWriteResult{{n: 2}},
		onWrite: cancel,
	}
	conn := NewConn(underlying)
	defer conn.Close()

	require.ErrorIs(t, conn.WriteWithContext(ctx, []byte("test")), context.Canceled)
	require.Equal(t, 1, underlying.writes)
	require.ErrorIs(t, conn.WriteWithContext(context.Background(), []byte("next")), ErrConnectionIsClosed)
	require.Equal(t, 1, underlying.writes)
}

func TestConnWriteWithContextQueuedWriteAfterPartialFailure(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	writeErr := errors.New("write failed")
	underlying := &scriptedWriteConn{
		Conn:    client,
		results: []scriptedWriteResult{{n: 2, err: writeErr}},
		onWrite: func() {
			close(started)
			<-release
		},
	}
	conn := NewConn(underlying)
	defer conn.Close()

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- conn.WriteWithContext(context.Background(), []byte("test")) }()
	<-started
	go func() { second <- conn.WriteWithContext(context.Background(), []byte("next")) }()
	close(release)
	require.ErrorIs(t, <-first, writeErr)
	require.ErrorIs(t, <-second, ErrConnectionIsClosed)
	require.Equal(t, 1, underlying.writes)
}

func TestConnWriteWithContextCancellationWhileWaiting(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	underlying := &scriptedWriteConn{
		Conn: client,
		onWrite: func() {
			close(started)
			<-release
		},
	}
	conn := NewConn(underlying)
	defer conn.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	first := make(chan error, 1)
	go func() { first <- conn.WriteWithContext(context.Background(), []byte("first")) }()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	observed := observedContext{Context: ctx, waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() { second <- conn.WriteWithContext(observed, []byte("second")) }()
	<-observed.waiting
	cancel()
	select {
	case err := <-second:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("canceled writer did not return while the first write was blocked")
	}
	close(release)
	require.NoError(t, <-first)
	require.Equal(t, 1, underlying.writes)
}
