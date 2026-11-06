package blockwise

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/stretchr/testify/require"
)

func TestDeliveredResponseCanBeReusedBeforeHandlerReturns(t *testing.T) {
	cc := newTestClient()
	bw := New(cc, time.Minute, func(err error) { t.Error(err) }, nil)
	response := pool.NewMessage(context.Background())
	response.SetCode(codes.Changed)
	response.SetToken([]byte("response"))
	writer := responsewriter.New(cc.AcquireMessage(context.Background()), cc)
	writer.Message().SetCode(codes.Changed)
	defer cc.ReleaseMessage(writer.Message())

	err := bw.handleReceivedMessage(writer, response, SZX1024, 65535,
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5683}, 1,
		func(_ *responsewriter.ResponseWriter[*testClient], delivered *pool.Message) {
			// A receiving application may immediately release and reuse its response.
			delivered.Reset()
			delivered.SetCode(codes.PUT)
			delivered.SetOptionUint32(message.Block1, 0)
		})
	require.NoError(t, err)
	require.False(t, writer.Message().HasOption(message.Block1),
		"reused response metadata must not be interpreted as a new request")
}
