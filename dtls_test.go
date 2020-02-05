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
	"testing"
	"time"

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
	c, err := tls.X509KeyPair(cert, key)
	require.NoError(t, err)

	config := &dtls.Config{
		Certificates:         []tls.Certificate{c},
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		ConnectTimeout:       dtls.ConnectTimeoutOption(30 * time.Second),
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
