package tcp_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v2/examples/dtls/pki"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/tcp"
	"github.com/plgd-dev/go-coap/v2/tcp/message/pool"
	"github.com/stretchr/testify/require"
)

func TestServer_CleanUpConns(t *testing.T) {
	ld, err := coapNet.NewTCPListener("tcp4", "")
	require.NoError(t, err)
	defer ld.Close()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := tcp.NewServer(tcp.WithOnNewClientConn(func(cc *tcp.ClientConn, tlsconn *tls.Conn) {
		require.Nil(t, tlsconn) // tcp without tls

		checkCloseWg.Add(1)
		cc.AddOnClose(func() {
			checkCloseWg.Done()
		})
	}))
	defer sd.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := tcp.Dial(ld.Addr().String())
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})
	defer cc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
}

func createTLSConfig(ctx context.Context) (serverConfig *tls.Config, clientConfig *tls.Config, clientSerial *big.Int, err error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return
	}
	// server cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "server@test.com")
	if err != nil {
		return
	}
	certificate, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return
	}
	// cert pool
	certPool, err := pki.LoadCertPool(rootBytes)
	if err != nil {
		return
	}

	serverConfig = &tls.Config{
		Certificates: []tls.Certificate{*certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}

	// client cert
	certBytes, keyBytes, err = pki.GenerateCertificate(ca, caPriv, "client@test.com")
	if err != nil {
		return
	}
	certificate, err = pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return
	}
	clientInfo, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return
	}
	clientSerial = clientInfo.SerialNumber

	clientConfig = &tls.Config{
		Certificates:       []tls.Certificate{*certificate},
		RootCAs:            certPool,
		InsecureSkipVerify: true,
	}

	return
}

func TestServer_SetContextValueWithPKI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverCgf, clientCgf, clientSerial, err := createTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewTLSListener("tcp4", "", serverCgf)
	require.NoError(t, err)
	defer ld.Close()

	onNewConn := func(cc *tcp.ClientConn, tlscon *tls.Conn) {
		require.NotNil(t, tlscon)
		// set connection context certificate
		clientCert := tlscon.ConnectionState().PeerCertificates[0]
		require.NoError(t, err)
		cc.Session().SetContextValue("client-cert", clientCert)
	}
	handle := func(w *tcp.ResponseWriter, r *pool.Message) {
		// get certificate from connection context
		clientCert := r.Context().Value("client-cert").(*x509.Certificate)
		require.Equal(t, clientCert.SerialNumber, clientSerial)
		require.NotNil(t, clientCert)
		w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("done")))
	}

	sd := tcp.NewServer(tcp.WithHandlerFunc(handle), tcp.WithOnNewClientConn(onNewConn))
	defer sd.Stop()
	go func() {
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := tcp.Dial(ld.Addr().String(), tcp.WithTLS(clientCgf))
	require.NoError(t, err)
	defer cc.Close()

	_, err = cc.Get(ctx, "/")
	require.NoError(t, err)
}
