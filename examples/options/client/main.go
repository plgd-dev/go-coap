package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
)

func main() {
	co, err := coapdtls.Dial("localhost:5688", coapdtls.NewDTLSClientOptions(
		piondtls.WithPSK(func(hint []byte) ([]byte, error) {
			fmt.Printf("Server's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		}),
		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Client")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
	))
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	defer co.Close()

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



































}	log.Printf("Response payload: %+v", resp)	}		log.Fatalf("Error sending request: %v", err)	if err != nil {	resp, err := co.Get(ctx, path)	defer cancel()	ctx, cancel := context.WithTimeout(context.Background(), time.Second)	}		path = os.Args[1]	if len(os.Args) > 1 {	path := "/a"	defer co.Close()	}		log.Fatalf("Error dialing: %v", err)	if err != nil {	))		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Client")),		}),			return []byte{0xAB, 0xC1, 0x23}, nil			fmt.Printf("Server's hint: %s \n", hint)		piondtls.WithPSK(func(hint []byte) ([]byte, error) {	co, err := coapdtls.Dial("localhost:5688", coapdtls.NewDTLSClientOptions(func main() {)	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"	piondtls "github.com/pion/dtls/v3"	"time"	"os"	"log"