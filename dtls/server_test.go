package dtls_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/dtls"
	"github.com/plgd-dev/go-coap/v2/examples/dtls/pki"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
	"github.com/stretchr/testify/require"
)

func TestServerCleanUpConns(t *testing.T) {
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	ld, err := coapNet.NewDTLSListener("udp4", "", dtlsCfg)
	require.NoError(t, err)
	defer func() {
		err := ld.Close()
		require.NoError(t, err)
	}()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := dtls.NewServer(dtls.WithOnNewClientConn(func(cc *client.ClientConn, dtlsConn *piondtls.Conn) {
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

	cc, err := dtls.Dial(ld.Addr().String(), dtlsCfg)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)
	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()
}

func createDTLSConfig(ctx context.Context) (serverConfig *piondtls.Config, clientConfig *piondtls.Config, clientSerial *big.Int, err error) {
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

	serverConfig = &piondtls.Config{
		Certificates:         []tls.Certificate{*certificate},
		ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret,
		ClientCAs:            certPool,
		ClientAuth:           piondtls.RequireAndVerifyClientCert,
		ConnectContextMaker: func() (context.Context, func()) {
			return context.WithTimeout(ctx, 30*time.Second)
		},
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

	clientConfig = &piondtls.Config{
		Certificates:         []tls.Certificate{*certificate},
		ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret,
		RootCAs:              certPool,
		InsecureSkipVerify:   true,
	}

	return
}

func TestServerSetContextValueWithPKI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3600)
	defer cancel()
	serverCgf, clientCgf, clientSerial, err := createDTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		err := ld.Close()
		require.NoError(t, err)
	}()

	onNewConn := func(cc *client.ClientConn, dtlsConn *piondtls.Conn) {
		// set connection context certificate
		clientCert, err := x509.ParseCertificate(dtlsConn.ConnectionState().PeerCertificates[0])
		require.NoError(t, err)
		cc.SetContextValue("client-cert", clientCert)
	}
	handle := func(w *client.ResponseWriter, r *pool.Message) {
		// get certificate from connection context
		clientCert := r.Context().Value("client-cert").(*x509.Certificate)
		require.Equal(t, clientCert.SerialNumber, clientSerial)
		require.NotNil(t, clientCert)
		err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("done")))
		require.NoError(t, err)
	}

	sd := dtls.NewServer(dtls.WithHandlerFunc(handle), dtls.WithOnNewClientConn(onNewConn))
	defer sd.Stop()
	go func() {
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf)
	require.NoError(t, err)

	_, err = cc.Get(ctx, "/")
	require.NoError(t, err)
	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()
}

func TestServerInactiveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		err := ld.Close()
		require.NoError(t, err)
	}()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, dtlsConn *piondtls.Conn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		dtls.WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			err := cc.Close()
			require.NoError(t, err)
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	err = cc.Ping(ctx)
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}

func TestServerKeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		err := ld.Close()
		require.NoError(t, err)
	}()

	var checkCloseWg sync.WaitGroup
	defer checkCloseWg.Wait()
	sd := dtls.NewServer(
		dtls.WithOnNewClientConn(func(cc *client.ClientConn, tlscon *piondtls.Conn) {
			checkCloseWg.Add(1)
			cc.AddOnClose(func() {
				checkCloseWg.Done()
			})
		}),
		dtls.WithKeepAlive(3, 100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			err := cc.Close()
			require.NoError(t, err)
		}),
		dtls.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		err := sd.Serve(ld)
		require.NoError(t, err)
	}()

	cc, err := dtls.Dial(
		ld.Addr().String(),
		clientCgf,
		dtls.WithGoPool(func(f func()) error {
			time.Sleep(time.Millisecond * 500)
			f()
			return nil
		}),
	)
	require.NoError(t, err)
	checkCloseWg.Add(1)
	cc.AddOnClose(func() {
		checkCloseWg.Done()
	})

	// send ping to create serverside connection
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cc.Ping(ctx)
	require.NoError(t, err)

	checkCloseWg.Wait()
	require.True(t, inactivityDetected)
}
