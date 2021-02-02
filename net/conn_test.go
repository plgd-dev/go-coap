package net

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConn_WriteWithContext(t *testing.T) {
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
			name: "cancelled",
			args: args{
				ctx:  ctxCanceled,
				data: helloWorld,
			},
			wantErr: true,
		},
	}

	listener, err := NewTCPListener("tcp", "127.0.0.1:", WithHeartBeat(time.Millisecond*100))
	assert.NoError(t, err)
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			conn, err := listener.AcceptWithContext(ctx)
			if err != nil {
				return
			}
			c := NewConn(conn, WithHeartBeat(time.Millisecond*10))
			b := make([]byte, len(helloWorld))
			_ = c.ReadFullWithContext(ctx, b)
			c.Close()
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tcpConn, err := net.Dial("tcp", listener.Addr().String())
			assert.NoError(t, err)
			c := NewConn(tcpConn, WithHeartBeat(time.Millisecond*100))
			defer c.Close()

			c.LocalAddr()
			c.RemoteAddr()

			err = c.WriteWithContext(tt.args.ctx, tt.args.data)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
