package main

import (
	"context"
	"crypto/tls"
	"log"
	"os"
	"time"

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v2/dtls"
	"github.com/plgd-dev/go-coap/v2/examples/dtls/pki"
)

func main() {
	config, err := createClientConfig(context.Background())
	if err != nil {
		log.Fatalln(err)
		return
	}
	co, err := dtls.Dial("localhost:5688", config)
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

func createClientConfig(ctx context.Context) (*piondtls.Config, error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return nil, err
	}
	// client cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "client@test.com")
	if err != nil {
		return nil, err
	}
	certificate, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return nil, err
	}
	// cert pool
	certPool, err := pki.LoadCertPool(rootBytes)
	if err != nil {
		return nil, err
	}

	return &piondtls.Config{
		Certificates:         []tls.Certificate{*certificate},
		ExtendedMasterSecret: piondtls.RequireExtendedMasterSecret,
		RootCAs:              certPool,
		InsecureSkipVerify:   true,
	}, nil
}
