package tcp_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/examples/dtls/pki"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/options/config"
	"github.com/plgd-dev/go-coap/v3/pkg/runner/periodic"
	"github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/plgd-dev/go-coap/v3/tcp/coder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
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

	sd := tcp.NewServer(options.WithOnNewConn(func(cc *client.Conn) {
		_, ok := cc.NetConn().(*tls.Conn)
		require.False(t, ok) // tcp without tls
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

func createTLSConfig() (serverConfig *tls.Config, clientConfig *tls.Config, clientSerial *big.Int, err error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	// server cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "server@test.com")
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	certificate, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	// cert pool
	certPool, err := pki.LoadCertPool(rootBytes)
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}

	serverConfig = &tls.Config{
		Certificates: []tls.Certificate{*certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}

	// client cert
	certBytes, keyBytes, err = pki.GenerateCertificate(ca, caPriv, "client@test.com")
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	certificate, err = pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	clientInfo, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return serverConfig, clientConfig, clientSerial, err
	}
	clientSerial = clientInfo.SerialNumber

	clientConfig = &tls.Config{
		Certificates:       []tls.Certificate{*certificate},
		RootCAs:            certPool,
		InsecureSkipVerify: true,
	}

	return serverConfig, clientConfig, clientSerial, err
}

func TestServerSetContextValueWithPKI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	serverCgf, clientCgf, clientSerial, err := createTLSConfig()
	require.NoError(t, err)

	ld, err := coapNet.NewTLSListener("tcp4", "", serverCgf)
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	onNewConn := func(cc *client.Conn) {
		require.NotNil(t, cc)
		tlscon, ok := cc.NetConn().(*tls.Conn)
		require.True(t, ok)
		// set connection context certificate
		clientCert := tlscon.ConnectionState().PeerCertificates[0]
		cc.SetContextValue("client-cert", clientCert)
	}
	handle := func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
		// get certificate from connection context
		clientCert := r.Context().Value("client-cert").(*x509.Certificate)
		require.Equal(t, clientCert.SerialNumber, clientSerial)
		require.NotNil(t, clientCert)
		errH := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("done")))
		require.NoError(t, errH)
	}

	sd := tcp.NewServer(options.WithHandlerFunc(handle), options.WithOnNewConn(onNewConn))
	defer sd.Stop()
	go func() {
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()

	cc, err := tcp.Dial(ld.Addr().String(), options.WithTLS(clientCgf))
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
	require.True(t, inactivityDetected.Load())
}

func TestServerKeepAliveMonitor(t *testing.T) {
	var inactivityDetected atomic.Bool

	ld, err := coapNet.NewTCPListener("tcp", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	checkClose := semaphore.NewWeighted(1)
	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)
	sd := tcp.NewServer(
		options.WithOnNewConn(func(cc *client.Conn) {
			cc.AddOnClose(func() {
				checkClose.Release(1)
			})
		}),
		options.WithKeepAlive(3, 500*time.Millisecond, func(cc *client.Conn) {
			require.False(t, inactivityDetected.Load())
			inactivityDetected.Store(true)
			errC := cc.Close()
			require.NoError(t, errC)
		}),
		options.WithPeriodicRunner(periodic.New(ctx.Done(), time.Millisecond*100)),
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

	cc, err := net.Dial("tcp", ld.Addr().String())
	require.NoError(t, err)
	defer func() {
		_ = cc.Close()
	}()

	p := pool.NewMessage(ctx)
	p.SetCode(codes.GET)
	err = p.SetPath("/tmp")
	require.NoError(t, err)

	data, err := p.MarshalWithEncoder(coder.DefaultCoder)
	require.NoError(t, err)
	_, err = cc.Write(data)
	require.NoError(t, err)

	err = checkClose.Acquire(ctx, 1)
	require.NoError(t, err)
	require.True(t, inactivityDetected.Load())
}

func TestCheckForLossOrder(t *testing.T) {
	ld, err := coapNet.NewTCPListener("tcp4", "")
	require.NoError(t, err)
	defer func() {
		errC := ld.Close()
		require.NoError(t, errC)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*8)
	defer cancel()

	const numMessages = 1000
	arrivedMessages := make([]uint64, 0, numMessages)
	var arrivedMessagesLock sync.Mutex

	sd := tcp.NewServer(options.WithHandlerFunc(func(resp *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
		errH := resp.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("1234")))
		require.NoError(t, errH)
		arrivedMessagesLock.Lock()
		defer arrivedMessagesLock.Unlock()
		arrivedMessages = append(arrivedMessages, binary.LittleEndian.Uint64(req.Token()))
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := sd.Serve(ld)
		assert.NoError(t, errS)
	}()
	defer func() {
		sd.Stop()
		wg.Wait()
	}()

	cc, err := tcp.Dial(ld.Addr().String())
	require.NoError(t, err)
	defer func() {
		errC := cc.Close()
		require.NoError(t, errC)
		<-cc.Done()
	}()
	for i := 0; i < numMessages; i++ {
		p, err := cc.NewGetRequest(ctx, "/tmp")
		require.NoError(t, err)
		bs := make([]byte, 8)
		binary.LittleEndian.PutUint64(bs, uint64(i))
		p.SetToken(bs)
		_, err = cc.Do(p)
		require.NoError(t, err)
	}
	arrivedMessagesLock.Lock()
	defer arrivedMessagesLock.Unlock()
	require.Len(t, arrivedMessages, numMessages)
	for idx, v := range arrivedMessages {
		require.Equal(t, uint64(idx), v)
	}
}
