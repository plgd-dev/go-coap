package dtls_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/net"
)

func ExampleConn_Get() {
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	conn, err := dtls.Dial("pluggedin.cloud:5684", dtlsCfg)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := conn.Get(ctx, "/oic/res")
	if err != nil {
		log.Fatal(err)
	}
	data, err := io.ReadAll(res.Body())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%v", data)
}

func ExampleDialWithOptions() {
	conn, err := dtls.DialWithOptions("pluggedin.cloud:5684", dtls.NewDTLSClientOptions(
		piondtls.WithPSK(func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		}),
		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Server")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := conn.Get(ctx, "/oic/res")
	if err != nil {
		log.Fatal(err)
	}
	data, err := io.ReadAll(res.Body())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%v", data)
}

func ExampleServer() {
	dtlsCfg := &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	l, err := net.NewDTLSListener("udp", "0.0.0.0:5683", dtlsCfg)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	s := dtls.NewServer()
	defer s.Stop()
	log.Fatal(s.Serve(l))
}

func ExampleServerWithOptions() {
	l, err := net.NewDTLSListenerWithOptions("udp", "0.0.0.0:5683", net.NewDTLSServerOptions(
		piondtls.WithPSK(func(hint []byte) ([]byte, error) {
			fmt.Printf("Hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		}),
		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Server")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	s := dtls.NewServer()
	defer s.Stop()
	log.Fatal(s.Serve(l))
}
