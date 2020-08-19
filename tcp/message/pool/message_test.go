package pool_test

import (
	"context"
	"io/ioutil"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/tcp/message/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_ConvertTo(t *testing.T) {
	msg := pool.AcquireMessage(context.Background())
	_, err := msg.Unmarshal([]byte{35, byte(codes.GET), 0x1, 0x2, 0x3, 0xff, 0x1})
	require.NoError(t, err)
	a, _ := pool.ConvertTo(msg)
	require.NoError(t, err)
	pool.ReleaseMessage(msg)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf, err := ioutil.ReadAll(a.Body)
		assert.NoError(t, err)
		assert.Equal(t, []byte{1}, buf)
	}()
	msg = pool.AcquireMessage(context.Background())
	_, err = msg.Unmarshal([]byte{35, byte(codes.GET), 0x1, 0x2, 0x3, 0xff, 0x2})
	require.NoError(t, err)
	wg.Wait()
}
