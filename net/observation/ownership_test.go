package observation

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/stretchr/testify/require"
)

type ownershipClient struct{}

func (*ownershipClient) Context() context.Context         { return context.Background() }
func (*ownershipClient) WriteMessage(*pool.Message) error { return nil }
func (*ownershipClient) ReleaseMessage(msg *pool.Message) { msg.Reset() }
func (*ownershipClient) AcquireMessage(ctx context.Context) *pool.Message {
	return pool.NewMessage(ctx)
}

func TestNotificationCleanupUsesMetadataBeforeCallback(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "notification", true: "terminal"}[terminal], func(t *testing.T) {
			cc := new(ownershipClient)
			handler := NewHandler(cc, nil, nil)
			token := message.Token("observe")
			called := 0
			observation := newObservation(message.Message{Code: codes.GET, Token: token}, handler,
				func(response *pool.Message) {
					called++
					cc.ReleaseMessage(response)
					if terminal {
						response.SetObserve(123)
					}
				}, make(chan respObservationMessage, 1))
			_, loaded := handler.observations.LoadOrStore(token.Hash(), observation)
			require.False(t, loaded)
			response := pool.NewMessage(context.Background())
			response.SetCode(codes.Content)
			response.SetToken(token)
			if !terminal {
				response.SetObserve(1)
			}
			require.True(t, handler.HandleNotification(response))
			require.Equal(t, 1, called)
			require.Equal(t, terminal, observation.Canceled())
		})
	}
}
