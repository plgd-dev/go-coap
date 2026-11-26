package blockwise

import (
	"bytes"
	"testing"
	"time"

	"github.com/dsnet/golib/memfile"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestBlock2CompletionPreservesInitialToken(t *testing.T) {
	r := newBlock1Receiver(t, time.Minute)
	initialToken := message.Token{1}
	finalToken := message.Token{2}
	cached := r.cc.AcquireMessage(t.Context())
	defer r.cc.ReleaseMessage(cached)
	cached.SetToken(initialToken)
	cached.SetCode(codes.Content)
	payload := memfile.New(nil)
	_, err := payload.Write(bytes.Repeat([]byte{0}, testBlock1Size))
	require.NoError(t, err)
	cached.SetBody(payload)
	request := r.cc.AcquireMessage(t.Context())
	defer r.cc.ReleaseMessage(request)
	request.SetToken(finalToken)
	request.SetCode(codes.Content)
	request.SetBody(bytes.NewReader([]byte("last")))
	w := responsewriter.New(r.cc.AcquireMessage(t.Context()), r.cc)
	defer func() { r.cc.ReleaseMessage(w.Message()) }()
	calls := 0
	err = r.bw.appendReceivedBlock(w, request, newRequestGuard(cached, 0, blockProtectionIdentity{}),
		func(_ *responsewriter.ResponseWriter[*testClient], msg *pool.Message) {
			calls++
			require.Equal(t, initialToken, msg.Token())
			body, readErr := msg.ReadBody()
			require.NoError(t, readErr)
			require.Equal(t, append(bytes.Repeat([]byte{0}, testBlock1Size), []byte("last")...), body)
		},
		receivedBlock{token: finalToken, blockType: message.Block2, sizeType: message.Size2}, nil,
		payload, testBlock1Size, testBlock1Size)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
