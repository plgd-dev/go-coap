package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestQBlockResponseDropped(t *testing.T) {
	p := pool.New(2, 1024)
	for _, value := range [][]byte{nil, make([]byte, 4)} {
		req := p.AcquireMessage(context.Background())
		resp := p.AcquireMessage(context.Background())
		req.SetCode(codes.Content)
		req.SetType(message.NonConfirmable)
		req.SetOptionBytes(message.QBlock2, value)
		cc := &Conn{}
		w := responsewriter.New(resp, cc)
		require.True(t, cc.handleDisabledQBlock(w, req))
		require.False(t, resp.IsModified())
		req.Remove(message.QBlock2)
		require.False(t, cc.handleDisabledQBlock(w, req))
		p.ReleaseMessage(req)
		p.ReleaseMessage(resp)
	}
}
