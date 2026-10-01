package udp_test

import (
	"context"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/test/qblocktransport"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/stretchr/testify/require"
)

func TestQBlockUDPCombinedFaults(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.PUT} {
		t.Run(method.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			t.Cleanup(cancel)
			controller := qblocktransport.NewController()
			qblocktransport.SaveTrace(t, "udp", controller, method)
			qc, qs := qblocktransport.Configs()
			deliveries := make(chan qblocktransport.Delivery, 32)
			qblocktransport.CheckDeliveries(t, deliveries)
			listener, err := coapNet.NewListenUDP("udp4", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			s := udp.NewServer(options.WithContext(ctx), options.WithQBlockServer(qs), options.WithMTU(qblocktransport.MTU), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithHandlerFunc(qblocktransport.Handler(deliveries)), options.WithErrors(func(err error) { t.Logf("UDP server: %v", err) }))
			done := make(chan error, 1)
			go func() { done <- s.Serve(listener) }()
			t.Cleanup(func() {
				s.Stop()
				_ = listener.Close()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(2 * time.Second):
					t.Error("UDP server shutdown deadline")
				}
			})
			relay, err := qblocktransport.NewUDPRelay(listener.LocalAddr().String(), controller)
			require.NoError(t, err)
			t.Cleanup(func() {
				_ = relay.Close()
				shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				require.NoError(t, relay.Wait(shutdown))
			})
			cc, err := udp.Dial(relay.Addr().String(), options.WithContext(ctx), options.WithQBlock(qc), options.WithMTU(qblocktransport.MTU), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithErrors(func(err error) { t.Logf("UDP client: %v", err) }))
			require.NoError(t, err)
			t.Cleanup(func() { _ = cc.Close() })
			qblocktransport.Run(t, ctx, cc, controller, method, deliveries)
		})
	}
}
