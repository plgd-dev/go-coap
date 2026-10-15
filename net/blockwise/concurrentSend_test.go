package blockwise

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/cache"
	"github.com/stretchr/testify/require"
)

// TestBlockWiseContinueSendingMessageConcurrent checks that serving block-wise
// response blocks for the same token from several goroutines does not race on
// the body of the message stored in sendingMessagesCache. It is meaningful
// under -race, which CI runs.
func TestBlockWiseContinueSendingMessageConcurrent(t *testing.T) {
	cli := newTestClient()
	b := New(cli, time.Hour, func(error) {}, nil)
	token := message.Token{1, 2, 3, 4}

	sending := cli.AcquireMessage(context.Background())
	sending.SetCode(codes.Content)
	sending.SetToken(token)
	sending.SetBody(bytes.NewReader(make([]byte, 8192)))
	b.sendingMessagesCache.LoadOrStore(token.Hash(), cache.NewElement(sending, time.Now().Add(time.Hour), nil))

	block, err := EncodeBlockOption(SZX16, 1, false)
	require.NoError(t, err)

	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r := cli.AcquireMessage(context.Background())
				r.SetCode(codes.GET)
				r.SetToken(token)
				r.SetOptionUint32(message.Block2, block)
				w := responsewriter.New(cli.AcquireMessage(context.Background()), cli)
				_, err := b.continueSendingMessage(w, r, SZX16, 1152, codes.Content)
				cli.ReleaseMessage(r)
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
