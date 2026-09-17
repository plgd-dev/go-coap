package client

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func TestConnTokenReservationRejectsLiveOwners(t *testing.T) {
	cfg := DefaultConfig
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
	token := message.Token{0x11}
	require.NoError(t, cc.claimToken(token, tokenOwnerRequest))
	require.Error(t, cc.claimToken(token, tokenOwnerQBlock))
	cc.releaseToken(token, tokenOwnerRequest)
	require.NoError(t, cc.claimToken(token, tokenOwnerQBlock))
}

func TestConnTokenReservationAllowsOnlyOneConcurrentClaim(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(previousProcs)

	for range 64 {
		cfg := DefaultConfig
		cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
		start := make(chan struct{})
		results := make(chan error, 64)
		var wg sync.WaitGroup
		for range 64 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results <- cc.claimToken(message.Token{0x14}, tokenOwnerRequest)
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		successes := 0
		for err := range results {
			if err == nil {
				successes++
			}
		}
		require.Equal(t, 1, successes)
	}
}

func TestConnReleaseAbsentTokenDoesNotPoisonFutureClaim(t *testing.T) {
	cfg := DefaultConfig
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
	token := message.Token{0x15}

	cc.releaseToken(token, tokenOwnerQBlock)
	require.NoError(t, cc.claimToken(token, tokenOwnerRequest))
}

func TestConnReleaseMismatchedOwnerDoesNotReleaseLiveReservation(t *testing.T) {
	cfg := DefaultConfig
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
	token := message.Token{0x16}
	require.NoError(t, cc.claimToken(token, tokenOwnerRequest))

	cc.releaseToken(token, tokenOwnerQBlock)
	require.Error(t, cc.claimToken(token, tokenOwnerQBlock))
	cc.releaseToken(token, tokenOwnerRequest)
	require.NoError(t, cc.claimToken(token, tokenOwnerQBlock))
}

func TestConnClaimFreshQBlockTokenCopiesAndReservesWinner(t *testing.T) {
	first := message.Token{0x21}
	second := message.Token{0x22}
	next := 0
	cfg := DefaultConfig
	cfg.GetToken = func() (message.Token, error) {
		next++
		if next == 1 {
			return first, nil
		}
		return second, nil
	}
	cc := NewConnWithOpts(&qblockTestSession{ctx: context.Background()}, &cfg)
	require.NoError(t, cc.claimToken(first, tokenOwnerRequest))

	token, err := cc.claimFreshQBlockToken()
	require.NoError(t, err)
	require.Equal(t, message.Token{0x22}, token)
	require.Equal(t, 2, next)

	token[0] = 0xff
	reservation, ok := cc.tokenReservations.Load(message.Token{0x22}.Hash())
	require.True(t, ok)
	require.Equal(t, message.Token{0x22}, reservation.token)
	require.Equal(t, tokenOwnerQBlock, reservation.owner)
}

func TestDoInternalRejectsQBlockReservedTokenWithoutWriting(t *testing.T) {
	session := &qblockTestSession{ctx: context.Background()}
	cfg := DefaultConfig
	cc := NewConnWithOpts(session, &cfg)
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	token := message.Token{0x13}
	req.SetToken(token)
	require.NoError(t, cc.claimToken(token, tokenOwnerQBlock))

	_, err := cc.doInternal(req)
	require.Error(t, err)
	require.Empty(t, session.writes)
}
