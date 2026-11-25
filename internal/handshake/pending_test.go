package handshake

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

type canceledSuccessConn struct {
	net.Conn
}

func (c *canceledSuccessConn) HandshakeContext(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func TestCanceledHandshakeCannotSucceed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pending := NewPending(1)
	attempt := pending.Admit(ctx)
	require.NotNil(t, attempt)
	peer, conn := net.Pipe()
	defer func() { _ = peer.Close() }()
	cancel()
	err := pending.Run(attempt, &canceledSuccessConn{Conn: conn}, 0)
	require.ErrorIs(t, err, context.Canceled)
	_, err = peer.Write([]byte{1})
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Nil(t, pending.Admit(ctx))
}
