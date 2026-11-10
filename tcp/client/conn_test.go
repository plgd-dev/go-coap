package client

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestUnprotectedErrorResponseOnlyTerminatesOSCOREExchange(t *testing.T) {
	for _, test := range []struct {
		name                string
		code                codes.Code
		wantErr             error
		wantRetained        bool
		wantProcessingError bool
	}{
		{name: "decryption error", code: codes.BadRequest, wantProcessingError: true},
		{name: "unknown context", code: codes.Unauthorized, wantProcessingError: true},
		{name: "decode error", code: codes.BadOption, wantProcessingError: true},
		{name: "application not found", code: codes.NotFound, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "application server error", code: codes.InternalServerError, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
		{name: "plaintext success", code: codes.Content, wantErr: oscore.ErrOSCORERequired, wantRetained: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			securityContext, err := oscore.NewContext(oscore.ContextConfig{
				MasterSecret: []byte("processing error response test"),
				SenderID:     []byte("client"),
				RecipientID:  []byte("server"),
			})
			require.NoError(t, err)
			request, exchange, err := securityContext.ProtectRequest(message.Message{
				Code: codes.GET, Token: message.Token{1},
			})
			require.NoError(t, err)
			key := string(request.Token)
			cc := &Conn{oscoreExchanges: map[string]tcpOSCOREExchange{
				key: {
					context:  securityContext,
					exchange: exchange,
					expires:  time.Now().Add(time.Minute),
				},
			}}
			response := pool.NewMessage(context.Background())
			response.SetMessage(message.Message{Code: test.code, Token: request.Token})

			err = cc.unprotectResponse(response)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			_, retained := cc.oscoreExchanges[key]
			require.Equal(t, test.wantRetained, retained)
			require.Equal(t, test.wantProcessingError, oscore.IsUnprotectedProcessingError(response.Context()))
		})
	}
}

func TestConnReceiveCleanupPreservesOwnerAcquisition(t *testing.T) {
	ctx := context.Background()
	session := &Session{messagePool: pool.New(1, 2048)}
	session.ctx.Store(&ctx)
	cc := &Conn{session: session}
	for i := 0; i < 64; i++ {
		req := cc.AcquireMessage(ctx)
		req.SetCode(codes.Content)
		var owned *pool.Message
		cc.ProcessReceivedMessageWithHandler(req, func(w *responsewriter.ResponseWriter[*Conn], r *pool.Message) {
			w.Message().SetModified(false)
			r.Hijack()
			cc.ReleaseMessage(r)
			owned = cc.AcquireMessage(ctx)
			owned.SetCode(codes.GET)
			owned.SetMessageID(42)
		})
		require.Equal(t, codes.GET, owned.Code(), "receive cleanup must not reset the owner's acquisition")
		require.Equal(t, int32(42), owned.MessageID())
		cc.ReleaseMessage(owned)
	}
}
