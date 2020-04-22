package keepalive

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type MockClientConn struct {
	pingErr error
	ctx     context.Context
}

func (cc *MockClientConn) Context() context.Context {
	return cc.ctx
}

func (cc *MockClientConn) Ping(context.Context) error {
	return cc.pingErr
}

func (cc *MockClientConn) Close() error {
	return nil
}

func TestKeepAlive_Client(t *testing.T) {
	cc := MockClientConn{context.DeadlineExceeded, context.Background()}
	k := New(WithConfig(Config{
		WaitForPong: time.Microsecond,
		Interval:    time.Millisecond * 100,
		NewRetryPolicy: func() RetryFunc {
			now := time.Now()
			return func() (time.Time, error) {
				c := time.Now()
				if c.Before(now.Add(time.Second * 2)) {
					return c.Add(time.Millisecond * 200), nil
				}
				return time.Time{}, ErrKeepAliveDeadlineExceeded
			}
		},
	}))
	err := k.Run(&cc)
	require.Equal(t, ErrKeepAliveDeadlineExceeded, err)
}
