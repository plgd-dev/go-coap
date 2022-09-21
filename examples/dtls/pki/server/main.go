package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"math/big"
	"time"

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/examples/dtls/pki"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

func onNewConn(cc *client.Conn) {
	dtlsConn, ok := cc.NetConn().(*piondtls.Conn)
	if !ok {
		log.Fatalf("invalid type %T", cc.NetConn())
	}
	clientCert, err := x509.ParseCertificate(dtlsConn.ConnectionState().PeerCertificates[0])
	if err != nil {
		log.Fatal(err)
	}
	cc.SetContextValue("client-cert", clientCert)
	cc.AddOnClose(func() {
		log.Println("closed connection")
	})
}

func toHexInt(n *big.Int) string {
	return fmt.Sprintf("%x", n) // or %X or upper case
}

func handleA(w mux.ResponseWriter, r *mux.Message) {
	clientCert := r.Context().Value("client-cert").(*x509.Certificate)
	log.Println("Serial number:", toHexInt(clientCert.SerialNumber))
	log.Println("Subject:", clientCert.Subject)
	log.Println("Email:", clientCert.EmailAddresses)

	log.Printf("got message in handleA:  %+v from %v\n", r, w.Conn().RemoteAddr())
	err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("A hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func main() {
	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(handleA))

	config, err := createServerConfig(context.Background())
	if err != nil {
		log.Fatalln(err)
		return
	}

	log.Fatal(listenAndServeDTLS("udp", ":5688", config, m))
}

func listenAndServeDTLS(network string, addr string, config *piondtls.Config, handler mux.Handler) error {
	l, err := net.NewDTLSListener(network, addr, config)
	if err != nil {
		return err
	}
	defer l.Close()
	s := dtls.NewServer(options.WithMux(handler), options.WithOnNewConn(onNewConn))
	return s.Serve(l)
}

func createServerConfig(ctx context.Context) (*piondtls.Config, error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return nil, err
	}
	// server cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "server@test.com")
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
		ClientCAs:            certPool,
		ClientAuth:           piondtls.RequireAndVerifyClientCert,
		ConnectContextMaker: func() (context.Context, func()) {
			return context.WithTimeout(ctx, 30*time.Second)
		},
	}, nil
}
