package dtls_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/examples/dtls/pki"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"
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
		errC := ld.Close()
		require.NoError(t, errC)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	defer func() {
		err = checkClose.Acquire(ctx, 2)
		require.NoError(t, err)
	}()
	sd := dtls.NewServer(options.WithOnNewConn(func(cc *client.Conn) {
		cc.AddOnClose(func() {
			checkClose.Release(1)
		})
	}))
	defer sd.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()

	cc, err := dtls.Dial(ld.Addr().String(), dtlsCfg)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
	require.NoError(t, err)
	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()
}

func createDTLSConfig() (serverConfig *piondtls.Config, clientConfig *piondtls.Config, clientSerial *big.Int, err error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	serverCgf, clientCgf, clientSerial, err := createDTLSConfig()
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	onNewConn := func(cc *client.Conn) {
		dtlsConn, ok := cc.NetConn().(*piondtls.Conn)
		assert.True(t, ok)
		errH := dtlsConn.HandshakeContext(ctx)
		assert.NoError(t, errH) //nolint:testifylint
		// set connection context certificate
		state, ok := dtlsConn.ConnectionState()
		assert.True(t, ok)
		clientCert, errP := x509.ParseCertificate(state.PeerCertificates[0])
		assert.NoError(t, errP) //nolint:testifylint
		cc.SetContextValue("client-cert", clientCert)
	}
	handle := func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		// get certificate from connection context
		clientCert := r.Context().Value("client-cert").(*x509.Certificate)
		assert.Equal(t, clientCert.SerialNumber, clientSerial)
		assert.NotNil(t, clientCert)
		errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("done")))
		assert.NoError(t, errH)
	}

	sd := dtls.NewServer(options.WithHandlerFunc(handle), options.WithOnNewConn(onNewConn))
	var wg sync.WaitGroup
	defer func() {
		sd.Stop()
		wg.Wait()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf)
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()

	_, err = cc.Get(ctx, "/")
	require.NoError(t, err)
}

func TestServerInactiveMonitor(t *testing.T) {
	var inactivityDetected atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig()
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := dtls.NewServer(
		options.WithOnNewConn(func(cc *client.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		options.WithInactivityMonitor(100*time.Millisecond, func(cc *client.Conn) {
			require.False(t, inactivityDetected.Load())
			inactivityDetected.Store(true)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
		options.WithReceivedMessageQueueSize(32),
		options.WithProcessReceivedMessageFunc(func(req *pool.Message, cc *client.Conn, handler config.HandlerFunc[*client.Conn]) {
			cc.ProcessReceivedMessageWithHandler(req, handler)
		}),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()

	cc, err := dtls.Dial(ld.Addr().String(), clientCgf)
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})

	// send ping to create serverside connection
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
	require.NoError(t, err)

	err = cc.Ping(ctxPing)
	require.NoError(t, err)

	time.Sleep(time.Second * 2)

	err = cc.Close()
	require.NoError(t, err)
	<-cc.Done()

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected.Load())
}

func TestServerKeepAliveMonitor(t *testing.T) {
	var inactivityDetected atomic.Bool

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	serverCgf, clientCgf, _, err := createDTLSConfig()
	require.NoError(t, err)

	ld, err := coapNet.NewDTLSListener("udp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(1)
	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)

	sd := dtls.NewServer(
		options.WithOnNewConn(func(cc *client.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		options.WithKeepAlive(3, 100*time.Millisecond, func(cc *client.Conn) {
			require.False(t, inactivityDetected.Load())
			inactivityDetected.Store(true)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
	)

	var serverWg sync.WaitGroup
	defer func() {
		sd.Stop()
		serverWg.Wait()
	}()
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()

	cc, err := piondtls.Dial("udp4", &net.UDPAddr{IP: []byte{127, 0, 0, 1}, Port: ld.Addr().(*net.UDPAddr).Port}, clientCgf)
	require.NoError(t, err)

	p := pool.NewMessage(ctx)
	p.SetCode(codes.GET)
	err = p.SetPath("/")
	require.NoError(t, err)
	p.SetMessageID(12345)
	p.SetType(message.NonConfirmable)

	data, err := p.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = cc.Write(data)
	require.NoError(t, err)

	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)
	require.True(t, inactivityDetected.Load())
}
