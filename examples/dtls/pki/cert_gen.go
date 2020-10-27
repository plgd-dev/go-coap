package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"time"
)

var (
	algo      = elliptic.P256()
	notBefore = time.Now()
	notAfter  = notBefore.Add(time.Hour)
	subject   = pkix.Name{
		Country:      []string{"BR"},
		Province:     []string{"Parana"},
		Locality:     []string{"Curitiba"},
		Organization: []string{"Test"},
		CommonName:   "test.com",
	}
)

func sequentialBytes(n int) io.Reader {
	sequence := make([]byte, n)
	for i := 0; i < n; i++ {
		sequence[i] = byte(i)
	}
	return bytes.NewReader(sequence)
}

// GenerateCA creates a deterministic certificate authority (for test purposes only)
func GenerateCA() (ca *x509.Certificate, cert, key []byte, priv *ecdsa.PrivateKey, err error) {
	priv, err = ecdsa.GenerateKey(algo, sequentialBytes(64))
	if err != nil {
		return
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(sequentialBytes(128), serialNumberLimit)

	ca = &x509.Certificate{
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		SerialNumber: serialNumber,

		Subject:        subject,
		EmailAddresses: []string{"ca@test.com"},
		IPAddresses:    []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},

		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &priv.PublicKey, priv)
	if err != nil {
		return
	}
	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return
	}
	key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return
}

// GenerateCertificate creates a certificate
func GenerateCertificate(ca *x509.Certificate, caPriv *ecdsa.PrivateKey, email string) (cert, key []byte, err error) {
	priv, err := ecdsa.GenerateKey(algo, rand.Reader)
	if err != nil {
		return
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return
	}

	template := x509.Certificate{
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		SerialNumber: serialNumber,

		Subject:        subject,
		EmailAddresses: []string{email},
		IPAddresses:    []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},

		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, ca, &priv.PublicKey, caPriv)
	if err != nil {
		return
	}

	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return
	}
	key = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return
}
