package net

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

type failingDTLSListener struct {
	err error
}

func (l *failingDTLSListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingDTLSListener) Close() error              { return nil }
func (l *failingDTLSListener) Addr() net.Addr            { return &net.UDPAddr{} }

func TestDTLSListenerAcceptErrorIdentity(t *testing.T) {
	other := errors.New("other error")
	for _, tt := range []struct {
		name   string
		err    error
		marked bool
	}{
		{name: "EOF", err: io.EOF, marked: true},
		{name: "wrapped EOF", err: fmt.Errorf("policy: %w", io.EOF), marked: true},
		{name: "other error", err: other},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := &DTLSListener{listener: &failingDTLSListener{err: tt.err}}
			conn, err := l.AcceptWithContext(context.Background())
			require.Nil(t, conn)
			require.ErrorIs(t, err, tt.err)
			var acceptErr *DTLSAcceptError
			require.Equal(t, tt.marked, errors.As(err, &acceptErr))
			require.NoError(t, l.Close())
			_, err = l.AcceptWithContext(context.Background())
			require.ErrorIs(t, err, ErrListenerIsClosed)
			require.NotErrorAs(t, err, &acceptErr)
		})
	}
}
