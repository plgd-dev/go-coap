package net

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/requestnotsent"
	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
	"github.com/stretchr/testify/require"
)

func TestWriteGateCancellationCauses(t *testing.T) {
	tests := []struct {
		name        string
		request     func() (context.Context, context.CancelFunc)
		close       bool
		timeout     time.Duration
		want        error
		wantTimeout bool
	}{
		{
			name: "request deadline",
			request: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
		{
			name: "server timeout",
			request: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			timeout: 100 * time.Millisecond, want: ErrWriteTimeout, wantTimeout: true,
		},
		{
			name: "request canceled",
			request: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "session closed",
			request: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			close: true, want: net.ErrClosed,
		},
	}
	for _, transport := range []string{"tcp", "udp"} {
		for _, tt := range tests {
			t.Run(transport+"/"+tt.name, func(t *testing.T) {
				req, cancelReq := tt.request()
				defer cancelReq()
				closeCtx, cancelClose := context.WithCancel(context.Background())
				defer cancelClose()
				ctx, cancel := writecontext.New(req, closeCtx, tt.timeout)
				defer cancel()
				if tt.close {
					cancelClose()
					<-ctx.Done()
				}
				var write func() error
				switch transport {
				case "tcp":
					raw, peer := net.Pipe()
					defer peer.Close()
					conn := NewConn(raw)
					defer conn.Close()
					conn.writeGate <- struct{}{}
					defer func() { <-conn.writeGate }()
					write = func() error { return conn.WriteWithContext(ctx, []byte("frame")) }
				case "udp":
					conn, err := NewListenUDP("udp4", "127.0.0.1:0")
					require.NoError(t, err)
					defer conn.Close()
					conn.writeOnce.Do(func() { conn.writeGate = make(chan struct{}, 1) })
					conn.writeGate <- struct{}{}
					defer func() { <-conn.writeGate }()
					addr := conn.LocalAddr().(*net.UDPAddr)
					write = func() error { return conn.WriteWithContext(ctx, addr, []byte("frame")) }
				}
				err := write()
				require.ErrorIs(t, err, tt.want)
				var notSent *requestnotsent.Error
				require.ErrorAs(t, err, &notSent)
				if tt.wantTimeout {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.NotErrorIs(t, err, ErrWriteTimeout)
				}
			})
		}
	}
}
