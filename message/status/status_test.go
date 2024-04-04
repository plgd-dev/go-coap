package status

import (
	"context"
	"errors"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestStatus(t *testing.T) {
	s, ok := FromError(nil)
	require.True(t, ok)
	require.Equal(t, OK, s.Code())

	_, ok = FromError(errors.New("test"))
	require.False(t, ok)

	msg := pool.NewMessage(context.TODO())
	msg.SetCode(codes.NotFound)
	err := Errorf(msg, "test %w", context.Canceled)
	s, ok = FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.NotFound, s.Code())
	require.ErrorIs(t, err, context.Canceled)

	s = Convert(err)
	require.Equal(t, codes.NotFound, s.Code())
	require.Equal(t, codes.NotFound, Code(err))

	require.Equal(t, OK, Code(nil))

	err = FromContextError(context.Canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, Canceled, Code(err))

	err = FromContextError(nil)
	require.Equal(t, OK, Code(err))

	err = FromContextError(context.DeadlineExceeded)
	require.Equal(t, Timeout, Code(err))

	err = FromContextError(errors.New("test"))
	require.Equal(t, Unknown, Code(err))
	require.Equal(t, Unknown, Code(errors.New("test")))
}
