package pki

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateCA(t *testing.T) {
	ca, cert, key, caPriv, err := GenerateCA()
	require.NoError(t, err)
	require.Contains(t, string(cert), "-----BEGIN CERTIFICATE-----")
	require.Contains(t, string(key), "-----BEGIN EC PRIVATE KEY-----")

	cert, key, err = GenerateCertificate(ca, caPriv, "cert@test.com")
	require.NoError(t, err)
	require.Contains(t, string(cert), "-----BEGIN CERTIFICATE-----")
	require.Contains(t, string(key), "-----BEGIN EC PRIVATE KEY-----")
}
