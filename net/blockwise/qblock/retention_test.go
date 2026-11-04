package qblock

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/stretchr/testify/require"
)

func TestQBlockServerRetentionCoversReceiveWindow(t *testing.T) {
	for _, age := range []uint32{0, 1, 10, 1000} {
		t.Run(fmt.Sprint(age), func(t *testing.T) {
			cc := newTestClient()
			engine := New(cc, func(err error) { t.Error(err) })
			defer engine.CloseQBlockTransfers()
			config := DefaultConfig()
			config.NonReceiveTimeout = 7 * time.Second
			require.NoError(t, engine.SetConfig(config))
			response := cc.AcquireMessage(context.Background())
			defer cc.ReleaseMessage(response)
			response.SetCode(codes.Content)
			response.SetOptionUint32(message.MaxAge, age)
			response.SetBody(bytes.NewReader(make([]byte, 32)))
			before := time.Now()
			require.NoError(t, engine.cacheQBlock2Response(context.Background(), "body", "identity",
				response, message.NonConfirmable))
			want := min(config.NonPartialTimeout(), max(config.NonReceiveTimeout, time.Duration(age)*time.Second))
			require.WithinDuration(t, before.Add(want), engine.q2Senders["body"].expires, time.Second)
			part, err := engine.buildQBlock2Response(context.Background(), "body", []byte("token"), SZX16, 1)
			require.NoError(t, err)
			defer cc.ReleaseMessage(part)
			gotAge, err := part.GetOptionUint32(message.MaxAge)
			require.NoError(t, err)
			require.Equal(t, age, gotAge, "retention must not increase cache freshness")
		})
	}
}
