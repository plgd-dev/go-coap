package main

import (
	"context"
	"fmt"
	"log"
	"net"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
)

func main() {
	raddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:5688")
	if err != nil {
		log.Fatalf("Error resolving UDP address: %v", err)
	}

	// Setup first UDP listener.
	udpconn, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Fatalf("Error establishing UDP listener: %v", err)
	}

	// Create DTLS client on UDP listener.
	client, err := piondtls.ClientWithOptions(udpconn, raddr,
		piondtls.WithPSK(func(hint []byte) ([]byte, error) {
			fmt.Printf("Server's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		}),
		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Client")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
		piondtls.WithConnectionIDGenerator(piondtls.OnlySendCIDGenerator()),
	)
	if err != nil {
		log.Fatalf("Error establishing DTLS client: %v", err)
	}
	co := dtls.Client(client)
	resp, err := co.Get(context.Background(), "/a")
	if err != nil {
		log.Fatalf("Error performing request: %v", err)
	}
	log.Printf("Response payload: %+v", resp)
	resp, err = co.Get(context.Background(), "/b")
	if err != nil {
		log.Fatalf("Error performing request: %v", err)
	}
	log.Printf("Response payload: %+v", resp)

	// Export state to resume connection from another address.
	state, ok := client.ConnectionState()
	if !ok {
		log.Fatalf("Error exporting DTLS state")
	}

	// Setup second UDP listener on a different address.
	udpconn, err = net.ListenUDP("udp", nil)
	if err != nil {
		log.Fatalf("Error establishing UDP listener: %v", err)
	}

	// Resume connection on new address with previous state.
	client, err = piondtls.ResumeWithOptions(&state, udpconn, raddr,
		piondtls.WithPSK(func(hint []byte) ([]byte, error) {
			fmt.Printf("Server's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		}),
		piondtls.WithPSKIdentityHint([]byte("Pion DTLS Client")),
		piondtls.WithCipherSuites(piondtls.TLS_PSK_WITH_AES_128_CCM_8),
		piondtls.WithConnectionIDGenerator(piondtls.OnlySendCIDGenerator()),
	)
	if err != nil {
		log.Fatalf("Error resuming DTLS connection: %v", err)
	}
	co = dtls.Client(client)
	// Requests can be performed without performing a second handshake.
	resp, err = co.Get(context.Background(), "/a")
	if err != nil {
		log.Fatalf("Error performing request: %v", err)
	}
	log.Printf("Response payload: %+v", resp)
	resp, err = co.Get(context.Background(), "/b")
	if err != nil {
		log.Fatalf("Error performing request: %v", err)
	}
	log.Printf("Response payload: %+v", resp)
}
