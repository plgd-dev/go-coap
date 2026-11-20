package client_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	coapTCP "github.com/plgd-dev/go-coap/v3/tcp"
	"github.com/plgd-dev/go-coap/v3/tcp/client"
	"github.com/stretchr/testify/require"
)

const libCoAPTLSTestServerName = "localhost"

type libCoAPTLSCredentials struct {
	serverConfig *tls.Config
	clientConfig *tls.Config
	caPath       string
	certPath     string
	keyPath      string
}

func TestLibCoAPClientOSCOREFromTLS(t *testing.T) {
	clientPath, err := findLibCoAPTLSBinary("coap-client", "coap-client-openssl", "coap-client-gnutls")
	if err != nil {
		requireLibCoAPOSCORE(t, "TLS-capable coap-client not found in PATH")
	}

	credentials := createLibCoAPTLSCredentials(t)
	listener, err := coapNet.NewTLSListener("tcp", "127.0.0.1:0", credentials.serverConfig)
	require.NoError(t, err)
	var handled atomic.Int32
	server := coapTCP.NewServer(
		options.WithOSCORE(newLibCoAPOSCOREProcessor(t, "server", "client")),
		options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], _ *pool.Message) {
			handled.Add(1)
			if responseErr := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("TLS OSCORE"))); responseErr != nil {
				t.Errorf("set TLS OSCORE response: %v", responseErr)
			}
		}),
	)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	defer func() {
		server.Stop()
		select {
		case serverErr := <-serverDone:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			require.Fail(t, "TLS OSCORE server did not stop")
		}
	}()

	configPath := writeLibCoAPOSCOREConfig(t, "client", "server")
	responsePath := t.TempDir() + "/tls-response.txt"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, clientPath, "-C", credentials.caPath, "-E", configPath, "-o", responsePath,
		"-B", "5", "coaps+tcp://"+listener.Addr().String()+"/interop")
	output, err := cmd.CombinedOutput()
	if err != nil && (strings.Contains(string(output), "OSCORE support not enabled") ||
		strings.Contains(string(output), "TLS support not enabled") ||
		strings.Contains(string(output), "DTLS support not enabled")) {
		requireLibCoAPOSCORE(t, "libcoap client lacks OSCORE or TLS support")
	}
	require.NoError(t, err, "libcoap TLS OSCORE client output: %s", output)
	require.NoError(t, ctx.Err())
	response, err := os.ReadFile(responsePath)
	require.NoError(t, err)
	require.Equal(t, []byte("TLS OSCORE"), response)
	require.EqualValues(t, 1, handled.Load())
}

func TestLibCoAPServerOSCOREFromTLS(t *testing.T) {
	serverPath, err := findLibCoAPTLSBinary("coap-server", "coap-server-openssl", "coap-server-gnutls")
	if err != nil {
		requireLibCoAPOSCORE(t, "TLS-capable "+err.Error())
	}
	credentials := createLibCoAPTLSCredentials(t)
	basePort, releasePorts := reserveLibCoAPServerPorts(t)
	serverConfig := writeLibCoAPOSCOREConfig(t, "server", "client")
	ctx, cancel := context.WithCancel(context.Background())
	server := exec.CommandContext(ctx, serverPath, "-A", "127.0.0.1", "-p", strconv.Itoa(basePort),
		"-v", "8", "-E", serverConfig, "-c", credentials.certPath, "-j", credentials.keyPath)
	var serverOutput lockedBuffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	releasePorts()
	require.NoError(t, server.Start())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	serverExited := false
	defer func() {
		if !serverExited {
			stopLibCoAPServerProcess(t, cancel, server, serverDone)
		}
	}()

	processor := newLibCoAPOSCOREProcessor(t, "client", "server")
	cc, response, exited, readyErr := waitForLibCoAPTLSServer(
		serverDone, net.JoinHostPort("127.0.0.1", strconv.Itoa(basePort+1)), credentials.clientConfig, processor, &serverOutput,
	)
	serverExited = exited
	if exited {
		cancel()
		if strings.Contains(serverOutput.String(), "OSCORE support not enabled") ||
			strings.Contains(serverOutput.String(), "TLS support not enabled") {
			requireLibCoAPOSCORE(t, "libcoap server lacks OSCORE or TLS support")
		}
		require.FailNow(t, "libcoap TLS server exited before becoming ready", "error: %v, output: %s", readyErr, serverOutput.String())
	}
	require.NoError(t, readyErr, "libcoap TLS output: %s", serverOutput.String())
	defer func() { require.NoError(t, cc.Close()) }()
	defer cc.ReleaseMessage(response)
	require.Equal(t, codes.Content, response.Code(), "libcoap TLS output: %s", serverOutput.String())
	body, err := response.ReadBody()
	require.NoError(t, err)
	require.NotEmpty(t, body)
}

func findLibCoAPTLSBinary(names ...string) (string, error) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("libcoap executable not found: %s", strings.Join(names, ", "))
}

func createLibCoAPTLSCredentials(t *testing.T) libCoAPTLSCredentials {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	now := time.Now()
	caTemplate := x509.Certificate{
		SerialNumber:          caSerial,
		Subject:               pkix.Name{CommonName: "go-coap libcoap test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCertificate, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)
	serverTemplate := x509.Certificate{
		SerialNumber:          serverSerial,
		Subject:               pkix.Name{CommonName: libCoAPTLSTestServerName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{libCoAPTLSTestServerName},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &serverTemplate, caCertificate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	require.NoError(t, err)
	serverPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certificate, err := tls.X509KeyPair(serverPEM, keyPEM)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(caCertificate)
	caPath, certPath, keyPath := t.TempDir()+"/test-ca.pem", t.TempDir()+"/server-cert.pem", t.TempDir()+"/server-key.pem"
	require.NoError(t, os.WriteFile(caPath, caPEM, 0o600))
	require.NoError(t, os.WriteFile(certPath, serverPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, 0o600))
	return libCoAPTLSCredentials{
		serverConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		},
		clientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: libCoAPTLSTestServerName,
			MinVersion: tls.VersionTLS12,
		},
		caPath:   caPath,
		certPath: certPath,
		keyPath:  keyPath,
	}
}

func waitForLibCoAPTLSServer(
	serverDone <-chan error,
	address string,
	tlsConfig *tls.Config,
	processor *oscore.Processor,
	serverOutput *lockedBuffer,
) (*client.Conn, *pool.Message, bool, error) {
	deadline := time.Now().Add(8 * time.Second)
	lastErr := fmt.Errorf("no TLS readiness attempt completed")
	for time.Now().Before(deadline) {
		select {
		case serverErr := <-serverDone:
			return nil, nil, true, libCoAPServerExitError(serverErr)
		default:
		}
		requestCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cc, err := coapTCP.Dial(
			address,
			options.WithContext(requestCtx),
			options.WithTLS(tlsConfig),
			options.WithOSCORE(processor),
		)
		if err == nil {
			request, requestErr := cc.NewGetRequest(requestCtx, "/time")
			if requestErr == nil {
				response, responseErr := cc.Do(request)
				cc.ReleaseMessage(request)
				if responseErr == nil {
					cancel()
					return cc, response, false, nil
				}
				err = responseErr
			} else {
				err = requestErr
			}
			_ = cc.Close()
		}
		cancel()
		lastErr = err
		select {
		case serverErr := <-serverDone:
			return nil, nil, true, libCoAPServerExitError(serverErr)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, nil, false, fmt.Errorf("libcoap TLS OSCORE server did not become ready: %w; output: %s",
		lastErr, serverOutput.String())
}
