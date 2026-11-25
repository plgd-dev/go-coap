package dtls_test

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"net"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/examples/dtls/pki"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/stretchr/testify/require"
)

func TestDialVerifiesDialledHost(t *testing.T) {
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	require.NoError(t, err)
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "server@test.com")
	require.NoError(t, err)
	cert, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	require.NoError(t, err)
	template, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	template.DNSNames = nil
	cert.Certificate[0], err = x509.CreateCertificate(rand.Reader, template, ca, template.PublicKey, caPriv)
	require.NoError(t, err)
	rootCAs, err := pki.LoadCertPool(rootBytes)
	require.NoError(t, err)

	l, err := coapNet.NewDTLSListener("udp4", "127.0.0.1:0", coapNet.NewDTLSServerOptions(piondtls.WithCertificates(*cert)))
	require.NoError(t, err)
	s := dtls.NewServer(options.WithErrors(func(error) {}))
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = s.Serve(l)
	}()
	defer func() {
		s.Stop()
		<-served
	}()

	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)

	ping := func(cc *client.Conn, err error) error {
		if err != nil {
			return err
		}
		defer func() {
			_ = cc.Close()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return cc.Ping(ctx)
	}
	udp4 := options.WithNetwork("udp4")
	quiet := options.WithErrors(func(error) {})

	withOptions := func(host string, dtlsOpts ...piondtls.ClientOption) error {
		dtlsOpts = append(dtlsOpts, piondtls.WithRootCAs(rootCAs), piondtls.WithInsecureSkipVerify(false))
		return ping(dtls.Dial(net.JoinHostPort(host, port), dtls.NewDTLSClientOptions(dtlsOpts...), udp4, quiet))
	}
	require.NoError(t, withOptions("127.0.0.1"))
	require.Error(t, withOptions("localhost"))
	require.NoError(t, withOptions("localhost", piondtls.WithServerName("127.0.0.1")))
	require.NoError(t, ping(dtls.Dial(
		net.JoinHostPort("localhost", port),
		dtls.NewDTLSClientOptions(
			piondtls.WithRootCAs(rootCAs),
			piondtls.WithInsecureSkipVerify(false),
		).WithoutHostnameVerification(),
		udp4,
		quiet,
	)))

	//nolint:staticcheck // Exercise the backward-compatible Dial config path.
	cfg := &piondtls.Config{RootCAs: rootCAs}
	require.Error(t, ping(dtls.Dial(net.JoinHostPort("localhost", port), cfg, udp4, quiet)))
	require.Empty(t, cfg.ServerName)
	cfg.ServerName = "127.0.0.1"
	require.NoError(t, ping(dtls.Dial(net.JoinHostPort("localhost", port), cfg, udp4, quiet)))
	require.Equal(t, "127.0.0.1", cfg.ServerName)
}
