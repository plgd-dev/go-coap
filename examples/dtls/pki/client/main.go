package main

import (
	"context"
	"log"
	"os"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/examples/dtls/pki"
)

func main() {
	dtlsOpts, err := createClientConfig()
	if err != nil {
		log.Fatalln(err)
		return
	}
	co, err := dtls.Dial("localhost:5688", dtlsOpts)
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	path := "/a"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := co.Get(ctx, path)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}
	log.Printf("Response payload: %+v", resp)
}

func createClientConfig() (dtls.DTLSClientOptions, error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return dtls.DTLSClientOptions{}, err
	}
	// client cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "client@test.com")
	if err != nil {
		return dtls.DTLSClientOptions{}, err
	}
	certificate, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return dtls.DTLSClientOptions{}, err
	}
	// cert pool
	certPool, err := pki.LoadCertPool(rootBytes)
	if err != nil {
		return dtls.DTLSClientOptions{}, err
	}

	return dtls.NewDTLSClientOptions(
		piondtls.WithCertificates(*certificate),
		piondtls.WithExtendedMasterSecret(piondtls.RequireExtendedMasterSecret),
		piondtls.WithRootCAs(certPool),
		piondtls.WithInsecureSkipVerify(false),
	), nil
}
