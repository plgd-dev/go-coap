package pool_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertTo(t *testing.T) {
	messagePool := pool.New(0, 0)
	msg := messagePool.AcquireMessage(context.Background())
	_, err := msg.Unmarshal([]byte{67, byte(codes.GET), 0, 0, 0x1, 0x2, 0x3, 0xff, 0x1})
	require.NoError(t, err)
	a, _ := pool.ConvertTo(msg)
	require.NoError(t, err)
	messagePool.ReleaseMessage(msg)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf, errR := io.ReadAll(a.Body)
		assert.NoError(t, errR)
		assert.Equal(t, []byte{1}, buf)
	}()
	msg = messagePool.AcquireMessage(context.Background())
	_, err = msg.Unmarshal([]byte{67, byte(codes.GET), 0, 0, 0x1, 0x2, 0x3, 0xff, 0x2})
	require.NoError(t, err)
	wg.Wait()
}
