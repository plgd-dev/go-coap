package qblock

import (
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/stretchr/testify/require"
)

// CoAP permits a zero-length request token; rejecting it must not prevent
// a Q2 server from retaining, repairing, and releasing the response body.
func TestManagerEmptyQ2Token(t *testing.T) {
	now := time.Unix(100, 0)
	m, err := NewManager(DefaultManagerConfig())
	require.NoError(t, err)
	meta := Metadata{Size: 16, SZX: 0, Identity: []byte("etag")}
	out, err := m.StartSender(OperationKey("get"), nil, Q2, meta, make([]byte, 16), now, 0)
	require.NoError(t, err)
	require.NotEmpty(t, out)
	id := out[0].TransferID
	_, err = m.StartSender(OperationKey("other"), nil, Q2, meta, make([]byte, 16), now, 0)
	require.ErrorIs(t, err, ErrTokenInUse)
	out, err = m.Control(Control{Missing: []uint32{0}}, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, outputNumbers(out))
	require.Contains(t, m.byToken, "")
	m.Cancel(id, nil)
	require.NotContains(t, m.byToken, "")
	require.Zero(t, m.retained)
	_, err = m.StartSender(OperationKey("other"), nil, Q2, meta, make([]byte, 16), now, 0)
	require.NoError(t, err)
}

func TestManagerEmptyQ2ControlAdmission(t *testing.T) {
	for _, bind := range []bool{false, true} {
		t.Run(map[bool]string{false: "control", true: "explicit_bind"}[bind], func(t *testing.T) {
			now := time.Unix(100, 0)
			m, err := NewManager(DefaultManagerConfig())
			require.NoError(t, err)
			meta := Metadata{Size: 16, SZX: 0, Identity: []byte("etag")}
			out, err := m.StartSender(OperationKey("get"), message.Token{1}, Q2, meta, make([]byte, 16), now, 0)
			require.NoError(t, err)
			id := out[0].TransferID
			if bind {
				require.NoError(t, m.BindToken(id, nil))
			}
			out, err = m.ControlWithToken(id, Control{Missing: []uint32{0}}, now)
			require.NoError(t, err)
			require.Equal(t, []uint32{0}, outputNumbers(out))
			require.Equal(t, id, m.byToken[""])
			_, err = m.StartSender(OperationKey("q1"), message.Token{2}, Q1, meta, make([]byte, 16), now, 0)
			require.NoError(t, err)
			q1ID := m.byToken[string(message.Token{2})]
			require.ErrorIs(t, m.BindToken(q1ID, nil), ErrUnknownTransfer)
			_, err = m.ControlWithToken(q1ID, Control{Missing: []uint32{0}}, now)
			require.ErrorIs(t, err, ErrInvalidControl)
			_, err = m.StartSender(OperationKey("invalid_q1"), nil, Q1, meta, make([]byte, 16), now, 0)
			require.ErrorIs(t, err, ErrUnknownTransfer)
		})
	}
}

func TestManagerEmptyQ2TokenLimitIsAtomic(t *testing.T) {
	now := time.Unix(100, 0)
	cfg := DefaultManagerConfig()
	cfg.MaxTokens = 1
	cfg.MaxTransfers = 1
	m, err := NewManager(cfg)
	require.NoError(t, err)
	meta := Metadata{Size: 16, SZX: 0, Identity: []byte("etag")}
	out, err := m.StartSender(OperationKey("get"), message.Token{1}, Q2, meta, make([]byte, 16), now, 0)
	require.NoError(t, err)
	id := out[0].TransferID
	_, err = m.ControlWithToken(id, Control{Missing: []uint32{0}}, now)
	require.ErrorIs(t, err, ErrLimitExceeded)
	require.NotContains(t, m.byToken, "")
	out, err = m.Control(Control{Token: message.Token{1}, Missing: []uint32{0}}, now)
	require.NoError(t, err)
	require.Equal(t, []uint32{0}, outputNumbers(out))
}
