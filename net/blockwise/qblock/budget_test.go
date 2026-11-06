package qblock

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

func TestQBlockEngineBudgetIsolation(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	first, second := New(newTestClient(), nil), New(newTestClient(), nil)
	require.True(t, first.qReserveBytes(qEngineMaxBytes))
	require.False(t, first.qReserveBytes(1))
	require.True(t, second.qReserveBytes(qEngineMaxBytes))
	for range qEngineMaxTransfers {
		require.True(t, first.qReserveTransfer())
	}
	require.False(t, first.qReserveTransfer())
	require.True(t, second.qReserveTransfer())
	require.Equal(t, bytesBefore+2*qEngineMaxBytes, qRetainedBytes.Load())
	require.Equal(t, transfersBefore+qEngineMaxTransfers+1, qRetainedTransfers.Load())
	first.qReleaseBytes(qEngineMaxBytes)
	second.qReleaseBytes(qEngineMaxBytes)
	for range qEngineMaxTransfers {
		first.qReleaseTransfer()
	}
	second.qReleaseTransfer()
	require.Zero(t, first.qRetainedBytes.Load())
	require.Zero(t, first.qRetainedTransfers.Load())
	require.Zero(t, second.qRetainedBytes.Load())
	require.Zero(t, second.qRetainedTransfers.Load())
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockGlobalBudgetFailureRollsBackEngine(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	reservedBytes := int64(qProcessMaxBytes) - bytesBefore
	reservedTransfers := int64(qProcessMaxTransfers) - transfersBefore
	qRetainedBytes.Add(reservedBytes)
	qRetainedTransfers.Add(reservedTransfers)
	defer qRetainedBytes.Add(-reservedBytes)
	defer qRetainedTransfers.Add(-reservedTransfers)
	b := New(newTestClient(), nil)
	require.False(t, b.qReserveBytes(1))
	require.False(t, b.qReserveTransfer())
	require.False(t, b.qReserveBytes(-1))
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.Equal(t, int64(qProcessMaxBytes), qRetainedBytes.Load())
	require.Equal(t, int64(qProcessMaxTransfers), qRetainedTransfers.Load())
}

func TestQBlockConcurrentEngineBudget(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	b := New(newTestClient(), nil)
	var wg sync.WaitGroup
	for range 256 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.qReserveBytes(1 << 20) {
				b.qReleaseBytes(1 << 20)
			}
			if b.qReserveTransfer() {
				b.qReleaseTransfer()
			}
		}()
	}
	wg.Wait()
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockCombinedBudgetLifecycle(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	b := New(newTestClient(), nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetType(message.NonConfirmable)
	p := q1Parameters{szx: SZX16, bodySize: 32, num: 0, more: true}
	transfer, err := b.q1TransferLocked(request, "incoming", p)
	require.NoError(t, err)
	transfer.expires = time.Now().Add(time.Hour)
	_, _, err = b.addQ1PayloadLocked("incoming", transfer, transfer.assembly, p, make([]byte, 16))
	require.NoError(t, err)
	_, duplicate, err := b.addQ1PayloadLocked("incoming", transfer, transfer.assembly, p, make([]byte, 16))
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, int64(16), b.qRetainedBytes.Load())

	response := pool.NewMessage(context.Background())
	response.SetBody(bytes.NewReader([]byte{1}))
	for i := 1; i < qEngineMaxTransfers; i++ {
		key := fmt.Sprint(i)
		require.NoError(t, b.cacheQBlock2Response(context.Background(), key, key, response, message.NonConfirmable))
	}
	require.Equal(t, int64(qEngineMaxTransfers), b.qRetainedTransfers.Load())
	bytesAtLimit := b.qRetainedBytes.Load()
	require.ErrorIs(t, b.cacheQBlock2Response(context.Background(), "overflow", "", response, message.NonConfirmable), ErrQBlockTooLarge)
	require.Equal(t, bytesAtLimit, b.qRetainedBytes.Load(), "failed slot reservation releases its bytes")

	b.CheckExpirations(time.Now().Add(2 * time.Hour))
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.NoError(t, b.cacheQBlock2Response(context.Background(), "again", "", response, message.NonConfirmable))
	b.CloseQBlockTransfers()
	b.CloseQBlockTransfers()
	require.ErrorIs(t, b.cacheQBlock2Response(context.Background(), "closed", "", response, message.NonConfirmable), context.Canceled)
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockBudgetValidationFailureCleanup(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	b := New(newTestClient(), nil)
	defer b.CloseQBlockTransfers()
	request := pool.NewMessage(context.Background())
	request.SetType(message.NonConfirmable)
	p := q1Parameters{szx: SZX16, bodySize: 32, num: 0, more: true}
	transfer, err := b.q1TransferLocked(request, "incoming", p)
	require.NoError(t, err)
	_, _, err = b.addQ1PayloadLocked("incoming", transfer, transfer.assembly, p, make([]byte, 16))
	require.NoError(t, err)
	p.num = 2
	p.more = false
	_, _, err = b.addQ1PayloadLocked("incoming", transfer, transfer.assembly, p, nil)
	require.ErrorIs(t, err, ErrQBlockInvalidNUM)
	require.Empty(t, b.q1Receivers)
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())

	response := pool.NewMessage(context.Background())
	response.SetBody(bytes.NewReader([]byte{1}))
	response.SetOptionBytes(message.ETag, make([]byte, 9))
	require.ErrorIs(t, b.cacheQBlock2Response(context.Background(), "invalid", "", response, message.NonConfirmable), ErrQBlockInvalidSize)
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}

func TestQBlockBudgetPreservesMaximumBody(t *testing.T) {
	bytesBefore, transfersBefore := qRetainedBytes.Load(), qRetainedTransfers.Load()
	b := New(newTestClient(), nil)
	defer b.CloseQBlockTransfers()
	response := pool.NewMessage(context.Background())
	response.SetBody(bytes.NewReader(make([]byte, q1DefaultMaxBodySize)))
	for _, key := range []string{"first", "second"} {
		require.NoError(t, b.cacheQBlock2Response(context.Background(), key, "", response, message.NonConfirmable))
	}
	require.Equal(t, int64(qEngineMaxBytes), b.qRetainedBytes.Load())
	response.SetBody(bytes.NewReader([]byte{1}))
	require.ErrorIs(t, b.cacheQBlock2Response(context.Background(), "overflow", "", response, message.NonConfirmable), ErrQBlockTooLarge)
	b.CloseQBlockTransfers()
	require.Zero(t, b.qRetainedBytes.Load())
	require.Zero(t, b.qRetainedTransfers.Load())
	require.Equal(t, bytesBefore, qRetainedBytes.Load())
	require.Equal(t, transfersBefore, qRetainedTransfers.Load())
}
