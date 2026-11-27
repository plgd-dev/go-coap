package net_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/internal/writecontext"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/stretchr/testify/require"
)

func TestTLSZeroByteWriteTimeoutClosesConnection(t *testing.T) {
	cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
	require.NoError(t, err)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	tlsServer := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
	tlsClient := tls.Client(client, &tls.Config{
		MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 || !bytes.Equal(state.PeerCertificates[0].Raw, cert.Certificate[0]) {
				return errors.New("unexpected test certificate")
			}
			return nil
		},
	})
	handshake := make(chan error, 1)
	go func() { handshake <- tlsServer.Handshake() }()
	require.NoError(t, tlsClient.Handshake())
	require.NoError(t, <-handshake)

	conn := coapNet.NewConn(tlsClient)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = conn.WriteWithContext(ctx, []byte("frame"))
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	require.True(t, netErr.Timeout())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, coapNet.ErrWriteTimeout)
	require.ErrorIs(t, conn.WriteWithContext(context.Background(), []byte("next")), coapNet.ErrConnectionIsClosed)
}

type tlsWriteObserver struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *tlsWriteObserver) Write(data []byte) (int, error) {
	if c.started != nil {
		c.once.Do(func() { close(c.started) })
	}
	return c.Conn.Write(data)
}

func TestTLSWriteContextCauses(t *testing.T) {
	const (
		requestDeadline = "request deadline"
		serverTimeout   = "server timeout"
		requestCancel   = "request cancel"
		sessionClose    = "session close"
	)
	for _, cause := range []string{requestDeadline, serverTimeout, requestCancel, sessionClose} {
		t.Run(cause, func(t *testing.T) {
			cert, err := tls.X509KeyPair(CertPEMBlock, KeyPEMBlock)
			require.NoError(t, err)
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			observed := &tlsWriteObserver{Conn: client}
			tlsServer := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12})
			tlsClient := tls.Client(observed, &tls.Config{
				MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12,
				InsecureSkipVerify: true,
				VerifyConnection: func(state tls.ConnectionState) error {
					if len(state.PeerCertificates) != 1 || !bytes.Equal(state.PeerCertificates[0].Raw, cert.Certificate[0]) {
						return errors.New("unexpected test certificate")
					}
					return nil
				},
			})
			handshake := make(chan error, 1)
			go func() { handshake <- tlsServer.Handshake() }()
			require.NoError(t, tlsClient.Handshake())
			require.NoError(t, <-handshake)
			observed.started = make(chan struct{})
			conn := coapNet.NewConn(tlsClient)
			defer conn.Close()
			req, cancelReq := context.WithCancel(context.Background())
			defer cancelReq()
			closeCtx, cancelClose := context.WithCancel(context.Background())
			defer cancelClose()
			timeout := time.Duration(0)
			if cause == requestDeadline {
				var cancel context.CancelFunc
				req, cancel = context.WithTimeout(req, 200*time.Millisecond)
				defer cancel()
			}
			if cause == serverTimeout {
				timeout = 200 * time.Millisecond
			}
			ctx, cancel := writecontext.New(req, closeCtx, timeout)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- conn.WriteWithContext(ctx, []byte("frame")) }()
			select {
			case <-observed.started:
			case <-time.After(2 * time.Second):
				t.Fatal("TLS write did not start")
			}
			switch cause {
			case requestCancel:
				cancelReq()
			case sessionClose:
				cancelClose()
			}
			select {
			case err := <-done:
				switch cause {
				case requestDeadline:
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.NotErrorIs(t, err, coapNet.ErrWriteTimeout)
				case serverTimeout:
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.ErrorIs(t, err, coapNet.ErrWriteTimeout)
				case requestCancel:
					require.ErrorIs(t, err, context.Canceled)
				case sessionClose:
					require.ErrorIs(t, err, context.Canceled)
					require.ErrorIs(t, err, net.ErrClosed)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("TLS write did not stop")
			}
		})
	}
}
