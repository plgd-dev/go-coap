package net

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
			name: "cancelled",
			args: args{
				ctx:  ctxCanceled,
				data: helloWorld,
			},
			wantErr: true,
		},
	}

	listener, err := NewTCPListener("tcp", "127.0.0.1:")
	assert.NoError(t, err)
	defer func() {
		err := listener.Close()
		assert.NoError(t, err)
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
			tcpConn, err := net.Dial("tcp", listener.Addr().String())
			assert.NoError(t, err)
			c := NewConn(tcpConn)
			defer func() {
				err := c.Close()
				assert.NoError(t, err)
			}()

			c.LocalAddr()
			c.RemoteAddr()

			err = c.WriteWithContext(tt.args.ctx, tt.args.data)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}
