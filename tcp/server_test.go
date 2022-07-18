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
	"github.com/plgd-dev/go-coap/v2/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/net/monitor/inactivity"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
	"github.com/plgd-dev/go-coap/v2/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v2/tcp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"
)

func TestServerCleanUpConns(t *testing.T) {
	ld, err := coapNet.NewTCPListener("tcp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	defer func() {
		errA := checkClose.Acquire(ctx, 2)
		require.NoError(t, errA)
	}()

	sd := tcp.NewServer(tcp.WithOnNewClientConn(func(cc *tcp.ClientConn, tlsconn *tls.Conn) {
		require.Nil(t, tlsconn) // tcp without tls
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
		require.NoError(t, errS)
	}()

	cc, err := tcp.Dial(ld.Addr().String())
	require.NoError(t, err)
	cc.AddOnClose(func() {
		checkClose.Release(1)
	})
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()
	ctxPing, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = cc.Ping(ctxPing)
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

func TestServerSetContextValueWithPKI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverCgf, clientCgf, clientSerial, err := createTLSConfig(ctx)
	require.NoError(t, err)

	ld, err := coapNet.NewTLSListener("tcp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	onNewConn := func(cc *tcp.ClientConn, tlscon *tls.Conn) {
		require.NotNil(t, tlscon)
		// set connection context certificate
		clientCert := tlscon.ConnectionState().PeerCertificates[0]
		cc.SetContextValue("client-cert", clientCert)
	}
	handle := func(w *responsewriter.ResponseWriter[*tcp.ClientConn], r *pool.Message) {
		// get certificate from connection context
		clientCert := r.Context().Value("client-cert").(*x509.Certificate)
		require.Equal(t, clientCert.SerialNumber, clientSerial)
		require.NotNil(t, clientCert)
		errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("done")))
		require.NoError(t, errH)
	}

	sd := tcp.NewServer(tcp.WithHandlerFunc(handle), tcp.WithOnNewClientConn(onNewConn))
	defer sd.Stop()
	go func() {
		errS := sd.Serve(ld)
		require.NoError(t, errS)
	}()

	cc, err := tcp.Dial(ld.Addr().String(), tcp.WithTLS(clientCgf))
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
	inactivityDetected := false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	ld, err := coapNet.NewTCPListener("tcp", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)

	sd := tcp.NewServer(
		tcp.WithOnNewClientConn(func(cc *tcp.ClientConn, tlscon *tls.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		tcp.WithInactivityMonitor(100*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		tcp.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*10)),
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
		require.NoError(t, errS)
	}()

	cc, err := tcp.Dial(
		ld.Addr().String(),
	)
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
	require.True(t, inactivityDetected)
}

func TestServerKeepAliveMonitor(t *testing.T) {
	inactivityDetected := false

	ld, err := coapNet.NewTCPListener("tcp", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	checkClose := semaphore.NewWeighted(2)
	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	sd := tcp.NewServer(
		tcp.WithOnNewClientConn(func(cc *tcp.ClientConn, tlscon *tls.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		tcp.WithKeepAlive(3, 200*time.Millisecond, func(cc inactivity.ClientConn) {
			require.False(t, inactivityDetected)
			inactivityDetected = true
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		tcp.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*100)),
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
		require.NoError(t, errS)
	}()

	cc, err := tcp.Dial(
		ld.Addr().String(),
		tcp.WithGoPool(func(f func()) error {
			time.Sleep(time.Second * 2)
			f()
			return nil
		}),
	)
	require.NoError(t, err)
	go func() {
		select {
		case <-cc.Done():
			checkClose.Release(1)
		case <-ctx.Done():
			return
		}
	}()

	// send ping to create serverside connection
	reqCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = cc.Get(reqCtx, "/tmp")
	require.Error(t, err)

	err = checkClose.Acquire(ctx, 2)
	require.NoError(t, err)
	require.True(t, inactivityDetected)
}
