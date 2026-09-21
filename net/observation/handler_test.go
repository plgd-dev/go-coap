package observation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

type observationTestClient struct {
	ctx      context.Context
	pool     *pool.Pool
	writeErr error
}

func (c *observationTestClient) Context() context.Context         { return c.ctx }
func (c *observationTestClient) WriteMessage(*pool.Message) error { return c.writeErr }
func (c *observationTestClient) ReleaseMessage(msg *pool.Message) { c.pool.ReleaseMessage(msg) }
func (c *observationTestClient) AcquireMessage(ctx context.Context) *pool.Message {
	return c.pool.AcquireMessage(ctx)
}

func newObservationHandlerForTest() (*Handler[*observationTestClient], *observationTestClient) {
	cc := &observationTestClient{ctx: context.Background(), pool: pool.New(8, 1024)}
	h := NewHandler(cc, func(*responsewriter.ResponseWriter[*observationTestClient], *pool.Message) {}, func(req *pool.Message) (*pool.Message, error) {
		resp := cc.AcquireMessage(req.Context())
		resp.SetCode(codes.Content)
		return resp, nil
	})
	return h, cc
}

func startObservationForTest(t *testing.T, h *Handler[*observationTestClient], cc *observationTestClient, token message.Token) *Observation[*observationTestClient] {
	t.Helper()
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	req.SetCode(codes.GET)
	req.SetToken(token)
	req.SetObserve(0)

	var (
		obs *Observation[*observationTestClient]
		err error
	)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		obs, err = h.NewObservation(req, func(*pool.Message) {})
	}()

	require.Eventually(t, func() bool {
		observation, ok := h.GetObservation(token.Hash())
		if !ok {
			return false
		}
		response := cc.AcquireMessage(context.Background())
		response.SetCode(codes.Content)
		response.SetToken(token)
		response.SetObserve(0)
		observation.handle(response)
		cc.ReleaseMessage(response)
		return true
	}, time.Second, time.Millisecond)
	wg.Wait()
	require.NoError(t, err)
	return obs
}

func TestObservationReleasesTokenReservationOnCleanup(t *testing.T) {
	claims := make(chan message.Token, 1)
	releases := make(chan message.Token, 1)
	h, cc := newObservationHandlerForTest()
	h.SetTokenCallbacks(func(token message.Token) error { claims <- token; return nil }, func(token message.Token) { releases <- token })
	obs := startObservationForTest(t, h, cc, message.Token{0x12})
	require.Equal(t, message.Token{0x12}, <-claims)
	require.NoError(t, obs.Cancel(context.Background()))
	require.Equal(t, message.Token{0x12}, <-releases)
}

func TestObservationWriteFailureCleansUpRegisteredObservation(t *testing.T) {
	writeErr := errors.New("write observation")
	h, cc := newObservationHandlerForTest()
	cc.writeErr = writeErr
	claims := make(chan message.Token, 1)
	releases := make(chan message.Token, 1)
	h.SetTokenCallbacks(func(token message.Token) error {
		claims <- token
		return nil
	}, func(token message.Token) {
		releases <- token
	})
	req := cc.AcquireMessage(context.Background())
	defer cc.ReleaseMessage(req)
	token := message.Token{0x13}
	req.SetCode(codes.GET)
	req.SetToken(token)
	req.SetObserve(0)

	obs, err := h.NewObservation(req, func(*pool.Message) {})

	require.Nil(t, obs)
	require.ErrorIs(t, err, writeErr)
	require.Equal(t, token, <-claims)
	require.Equal(t, token, <-releases)
	_, ok := h.GetObservation(token.Hash())
	require.False(t, ok)
}
