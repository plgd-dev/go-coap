package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"log"
	"math/big"
	"time"

	piondtls "github.com/pion/dtls/v3"
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	// force handshake otherwhise ConnectionState is not available
	err := dtlsConn.HandshakeContext(ctx)
	if err != nil {
		log.Fatalf("handshake failed: %v", err)
	}
	state, ok := dtlsConn.ConnectionState()
	if !ok {
		log.Fatalf("cannot get connection state")
	}
	clientCert, err := x509.ParseCertificate(state.PeerCertificates[0])
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

	dtlsOpts, err := createServerConfig()
	if err != nil {
		log.Fatalln(err)
		return
	}

	log.Fatal(listenAndServeDTLS("udp", ":5688", dtlsOpts, m))
}

func listenAndServeDTLS(network string, addr string, dtlsOpts net.DTLSServerOptions, handler mux.Handler) error {
	l, err := net.NewDTLSListener(network, addr, dtlsOpts)
	if err != nil {
		return err
	}
	defer l.Close()
	s := dtls.NewServer(options.WithMux(handler), options.WithOnNewConn(onNewConn))
	return s.Serve(l)
}

func createServerConfig() (net.DTLSServerOptions, error) {
	// root cert
	ca, rootBytes, _, caPriv, err := pki.GenerateCA()
	if err != nil {
		return net.DTLSServerOptions{}, err
	}
	// server cert
	certBytes, keyBytes, err := pki.GenerateCertificate(ca, caPriv, "server@test.com")
	if err != nil {
		return net.DTLSServerOptions{}, err
	}
	certificate, err := pki.LoadKeyAndCertificate(keyBytes, certBytes)
	if err != nil {
		return net.DTLSServerOptions{}, err
	}
	// cert pool
	certPool, err := pki.LoadCertPool(rootBytes)
	if err != nil {
		return net.DTLSServerOptions{}, err
	}

	return net.NewDTLSServerOptions(
		piondtls.WithCertificates(*certificate),
		piondtls.WithExtendedMasterSecret(piondtls.RequireExtendedMasterSecret),
		piondtls.WithClientCAs(certPool),
		piondtls.WithClientAuth(piondtls.RequireAndVerifyClientCert),
	), nil
}
