package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

type failingQBlockSeeker struct{}

func (failingQBlockSeeker) Read([]byte) (int, error) { return 0, io.EOF }
func (failingQBlockSeeker) Seek(int64, int) (int64, error) {
	return 0, errors.New("seek failed")
}

func TestQBlockDatagramSizePreservesBodyCursor(t *testing.T) {
	for _, tt := range []struct {
		name  string
		token message.Token
		body  string
		want  uint64
	}{
		{name: "empty", token: message.Token{1}, want: 5},
		{name: "payload", token: message.Token{1}, body: "abc", want: 9},
		{name: "long token", token: message.Token{1, 2, 3, 4, 5, 6, 7, 8}, body: "abc", want: 16},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := pool.NewMessage(context.Background())
			msg.SetType(message.NonConfirmable)
			msg.SetCode(codes.GET)
			msg.SetMessageID(1)
			msg.SetToken(tt.token)
			if tt.body != "" {
				msg.SetBody(bytes.NewReader([]byte(tt.body)))
			}
			size, err := qblockDatagramSize(msg)
			require.NoError(t, err)
			require.Equal(t, tt.want, size)
			wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
			require.NoError(t, err)
			require.Equal(t, uint64(len(wire)), size)
		})
	}

	msg := pool.NewMessage(context.Background())
	msg.SetType(message.NonConfirmable)
	msg.SetCode(codes.GET)
	msg.SetMessageID(2)
	msg.SetToken(message.Token{1})
	msg.SetOptionString(message.URIPath, "route")
	body := bytes.NewReader([]byte("abcdef"))
	msg.SetBody(body)
	_, err := body.Seek(2, io.SeekStart)
	require.NoError(t, err)
	size, err := qblockDatagramSize(msg)
	require.NoError(t, err)
	require.Equal(t, uint64(4+1+6+1+6), size)
	pos, err := body.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, int64(2), pos)
	remaining, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "cdef", string(remaining))
	_, err = body.Seek(2, io.SeekStart)
	require.NoError(t, err)
	wire, err := msg.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	require.Equal(t, uint64(len(wire)), size)

	msg.SetBody(failingQBlockSeeker{})
	_, err = qblockDatagramSize(msg)
	require.Error(t, err)
	msg.SetBody(nil)
	msg.SetToken(message.Token{1, 2, 3, 4, 5, 6, 7, 8, 9})
	_, err = qblockDatagramSize(msg)
	require.Error(t, err)
}
