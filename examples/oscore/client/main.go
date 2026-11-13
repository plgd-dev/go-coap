package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/plgd-dev/go-coap/v3/net/oscore"
	oscorestore "github.com/plgd-dev/go-coap/v3/net/oscore/store"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5688", "UDP server address")
	stateDir := flag.String("state-dir", "./oscore-state", "directory for durable OSCORE state")
	path := flag.String("path", "/hello", "CoAP resource path")
	flag.Parse()

	masterSecret, err := loadMasterSecret()
	if err != nil {
		log.Fatal(err)
	}
	state, err := oscorestore.NewHighWaterFileStore(*stateDir, "go-coap-example-client")
	if err != nil {
		log.Fatalf("create OSCORE state store: %v", err)
	}
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:        masterSecret,
		SenderID:            []byte("client"),
		RecipientID:         []byte("server"),
		SenderSequenceStore: state,
		ReplayWindowStore:   state,
	})
	if err != nil {
		log.Fatalf("create OSCORE context: %v", err)
	}
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:        []*oscore.Context{securityContext},
		PlaintextPolicy: oscore.PlaintextDisabled,
	})
	if err != nil {
		log.Fatalf("create OSCORE processor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cc, err := udp.Dial(*addr, options.WithOSCORE(processor), options.WithContext(ctx))
	if err != nil {
		log.Fatalf("dial OSCORE server: %v", err)
	}
	defer func() {
		if err := cc.Close(); err != nil {
			log.Printf("close client: %v", err)
		}
	}()

	response, err := cc.Get(ctx, *path)
	if err != nil {
		log.Fatalf("send protected GET request: %v", err)
	}
	defer cc.ReleaseMessage(response)
	body, err := response.ReadBody()
	if err != nil {
		log.Fatalf("read OSCORE response: %v", err)
	}
	log.Printf("OSCORE response (%v): %s", response.Code(), body)
}

func loadMasterSecret() ([]byte, error) {
	value := os.Getenv("GOCOAP_OSCORE_MASTER_SECRET")
	if value == "" {
		return nil, errors.New("set GOCOAP_OSCORE_MASTER_SECRET to the shared master secret in hexadecimal")
	}
	masterSecret, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode GOCOAP_OSCORE_MASTER_SECRET: %w", err)
	}
	if len(masterSecret) == 0 {
		return nil, errors.New("GOCOAP_OSCORE_MASTER_SECRET must not be empty")
	}
	return masterSecret, nil
}
