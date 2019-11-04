package coap

import (
	"crypto/ecdsa"
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

	"github.com/pion/dtls"
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

func generateRSACertsPEM(t *testing.T) ([]byte, []byte) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
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

func TestRSACerts(t *testing.T) {
	cert, key := generateRSACertsPEM(t)
	keys, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(keys.Certificate[0])
	require.NoError(t, err)
	privateKey := keys.PrivateKey

	config := &dtls.Config{
		Certificate:          certificate,
		PrivateKey:           privateKey,
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ConnectTimeout:       dtls.ConnectTimeoutOption(30 * time.Second),
		InsecureSkipVerify:   true,
		ClientAuth:           dtls.RequireAnyClientCert,
	}

	listenStarts := make(chan struct{})
	mux := NewServeMux()
	err = mux.Handle("/test", HandlerFunc(EchoServer))
	require.NoError(t, err)

	s := &Server{
		Net:        "udp-dtls",
		Addr:       ":5688",
		DTLSConfig: config,
		Handler:    mux,
		NotifyStartedFunc: func() {
			close(listenStarts)
		},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.ListenAndServe()
		require.Error(t, err)
	}()

	<-listenStarts

	connectTimeout := time.Second

	clientCfg := &dtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Client's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Client"),
		CipherSuites:    []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_CCM_8},
		ConnectTimeout:  &connectTimeout,
	}

	_, err = DialDTLS("udp-dtls", "localhost:5688", clientCfg)
	require.Error(t, err)
}
