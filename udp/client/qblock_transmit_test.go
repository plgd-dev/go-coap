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
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/require"
)

// Passing the whole datagram limit to EncodeMissing would write an oversized
// report; budgeting only payload bytes would also omit variable token overhead.
func TestQBlockMissingReportDatagramBudget(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mtu     uint16
		token   message.Token
		want    []byte
		wantErr bool
	}{
		{name: "exact fit", mtu: 13, token: message.Token{1}, want: []byte{0, 1, 0x18, 24}},
		{name: "truncate at CBOR boundary", mtu: 11, token: message.Token{1}, want: []byte{0, 1}},
		{name: "long token", mtu: 18, token: message.Token{1, 2, 3, 4, 5, 6, 7, 8}, want: []byte{0, 1}},
		{name: "no payload space", mtu: 8, token: message.Token{1}, wantErr: true},
		{name: "one integer cannot fit", mtu: 10, token: message.Token{1}, wantErr: true},
		{name: "zero budget", mtu: 0, token: message.Token{1}, wantErr: true},
		{name: "session limit", mtu: 3000, token: message.Token{1}, want: []byte{0, 1, 0x18, 24}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session := &qblockTestSession{ctx: context.Background()}
			cfg := DefaultConfig
			cfg.MTU = tt.mtu
			cc := NewConnWithOpts(session, &cfg, withQBlockClient(qblockClientConfig{Manager: qblock.DefaultManagerConfig(), ScheduleMode: qblockScheduleManual}))
			t.Cleanup(session.closeForTest)
			numbers := []uint32{0, 1, 24}
			if tt.name == "one integer cannot fit" {
				numbers = []uint32{24}
			}
			if tt.name == "session limit" {
				// One-byte numbers cannot exceed 24 distinct entries. Repeated
				// five-byte CBOR integers exercise the independent session cap.
				numbers = make([]uint32, 500)
				for i := range numbers {
					numbers[i] = 65536 + uint32(i)
				}
			}
			err := cc.qblockClient.writeServerQ1Control(context.Background(), tt.token, 1, blockwise.SZX16, qblock.Action{Kind: qblock.RequestMissing, Numbers: numbers}, 0)
			writes := session.writesSnapshot()
			if tt.wantErr {
				require.Error(t, err)
				require.Empty(t, writes)
				return
			}
			require.NoError(t, err)
			require.Len(t, writes, 1)
			wire, err := coder.DefaultCoder.Size(message.Message{Token: writes[0].token, Options: writes[0].options, Payload: writes[0].payload})
			require.NoError(t, err)
			require.LessOrEqual(t, wire, int(tt.mtu))
			require.LessOrEqual(t, wire, 2048)
			if tt.name == "session limit" {
				// 4 header + 1 token + 3 option + 1 marker + 407*5 = 2044.
				require.Len(t, writes[0].payload, 2035)
				decoded, err := qblock.DecodeMissing(writes[0].payload, 70000, 500)
				require.NoError(t, err)
				require.Len(t, decoded, 407)
			} else {
				require.Equal(t, tt.want, writes[0].payload)
			}
		})
	}
}

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
