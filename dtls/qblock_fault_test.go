package dtls_test

import (
	"context"
	"net"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/internal/test/qblocktransport"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/stretchr/testify/require"
)

type qFaultDTLSListener struct {
	*coapNet.DTLSListener
	controller *qblocktransport.Controller
}

func (l *qFaultDTLSListener) AcceptWithContext(ctx context.Context) (net.Conn, error) {
	c, err := l.DTLSListener.AcceptWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return qblocktransport.NewPlaintextConn(c, l.controller), nil
}

func TestQBlockDTLSCombinedFaults(t *testing.T) {
	for _, method := range []codes.Code{codes.GET, codes.POST, codes.PUT} {
		t.Run(method.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			t.Cleanup(cancel)
			controller := qblocktransport.NewController()
			qblocktransport.SaveTrace(t, "dtls", controller, method)
			qc, qs := qblocktransport.Configs()
			deliveries := make(chan qblocktransport.Delivery, 32)
			qblocktransport.CheckDeliveries(t, deliveries)
			base, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", qDTLSConfig())
			require.NoError(t, err)
			t.Cleanup(func() { _ = base.Close() })
			listener := &qFaultDTLSListener{base, controller}
			s := dtls.NewServer(options.WithContext(ctx), options.WithQBlockServer(qs), options.WithMTU(qblocktransport.MTU), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithDTLSHandshakeTimeout(3*time.Second), options.WithHandlerFunc(qblocktransport.Handler(deliveries)), options.WithErrors(func(err error) { t.Logf("DTLS server: %v", err) }))
			done := make(chan error, 1)
			go func() { done <- s.Serve(listener) }()
			t.Cleanup(func() {
				s.Stop()
				_ = base.Close()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(2 * time.Second):
					t.Error("DTLS server shutdown deadline")
				}
			})
			cc, err := dtls.Dial(base.Addr().String(), qDTLSConfig(), options.WithContext(ctx), options.WithQBlock(qc), options.WithMTU(qblocktransport.MTU), options.WithBlockwise(false, blockwise.SZX16, time.Second), options.WithErrors(func(err error) { t.Logf("DTLS client: %v", err) }))
			require.NoError(t, err)
			t.Cleanup(func() { _ = cc.Close() })
			qblocktransport.Run(t, ctx, cc, controller, method, deliveries)
			transport, ok := cc.NetConn().(*piondtls.Conn)
			require.True(t, ok, "public Dial must use the real DTLS transport")
			state, connected := transport.ConnectionState()
			require.True(t, connected, "fault workflow must complete an authenticated handshake")
			require.Equal(t, piondtls.TLS_PSK_WITH_AES_128_CCM_8, state.CipherSuiteID)
		})
	}
}
