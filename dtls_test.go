package coap

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	coapNet "github.com/go-ocf/go-coap/net"
	dtls "github.com/pion/dtls/v2"
	"github.com/stretchr/testify/require"
)

func TestServingDTLS(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", false, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx16(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx16, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx32(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx32, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx64(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx64, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx128(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx128, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx256(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx256, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx512(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx512, make([]byte, 128), simpleMsg)
}

func TestServingDTLSBlockWiseSzx1024(t *testing.T) {
	testServingTCPWithMsg(t, "udp-dtls", true, BlockWiseSzx1024, make([]byte, 128), simpleMsg)
}

func publicKey(priv interface{}) interface{} {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &k.PublicKey
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	default:
		return nil
	}
}

func pemBlockForKey(priv interface{}) *pem.Block {
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		return &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Unable to marshal ECDSA private key: %v", err)
			os.Exit(2)
		}
		return &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	default:
		return nil
	}
}

func generateCertsPEM(t *testing.T, generateKeyFunc func() (crypto.PrivateKey, error)) ([]byte, []byte) {
	priv, err := generateKeyFunc()
	if err != nil {
		require.NoError(t, err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Acme CEncodeToMemoryo"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 180),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey(priv), priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}), pem.EncodeToMemory(pemBlockForKey(priv))
}

func generateRSACertsPEM(t *testing.T) ([]byte, []byte) {
	return generateCertsPEM(t, func() (crypto.PrivateKey, error) {
		return rsa.GenerateKey(rand.Reader, 2048)
	})
}

func generateECDSACertsPEM(t *testing.T) ([]byte, []byte) {
	return generateCertsPEM(t, func() (crypto.PrivateKey, error) {
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	})
}

func TestRSACerts(t *testing.T) {
	cert, key := generateRSACertsPEM(t)
	c, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)

	config := &dtls.Config{
		Certificates:         []tls.Certificate{c},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		InsecureSkipVerify:   true,
		ClientAuth:           dtls.RequireAnyClientCert,
	}

	s := &Server{
		Net:        "udp-dtls",
		Addr:       ":5688",
		DTLSConfig: config,
		Handler:    HandlerFunc(EchoServer),
	}
	err = s.ListenAndServe()
	require.Error(t, err)
}

func TestECDSACerts_PeerCertificate(t *testing.T) {
	cert, key := generateECDSACertsPEM(t)
	c, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)

	config := dtls.Config{
		Certificates:       []tls.Certificate{c},
		CipherSuites:       []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_CCM_8},
		InsecureSkipVerify: true,
		ClientAuth:         dtls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			return nil
		},
	}
	l, err := coapNet.NewDTLSListener("udp", ":", &config, time.Millisecond*100)
	require.NoError(t, err)

	s := &Server{
		Net:       "udp-dtls",
		Listener:  l,
		KeepAlive: MustMakeKeepAlive(time.Second),
	}
	s.Handler = HandlerFunc(func(w ResponseWriter, r *Request) {
		certs := r.Client.PeerCertificates()
		require.NotEmpty(t, certs)
		err := s.Shutdown()
		require.NoError(t, err)
	})
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()
		s.ActivateAndServe()
	}()

	conn, err := DialDTLS("udp-dtls", l.Addr().String(), &config)
	require.NoError(t, err)
	certs := conn.PeerCertificates()
	require.NotEmpty(t, certs)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	_, err = conn.GetWithContext(ctx, "/")
	require.Error(t, err)
	conn.Close()
}
