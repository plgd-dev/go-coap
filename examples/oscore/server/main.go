package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/oscore"
	oscorestore "github.com/plgd-dev/go-coap/v3/net/oscore/store"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

func main() {
	addr := flag.String("addr", ":5688", "UDP listen address")
	stateDir := flag.String("state-dir", "./oscore-state", "directory for durable OSCORE state")
	flag.Parse()

	masterSecret, err := loadMasterSecret()
	if err != nil {
		log.Fatal(err)
	}
	state, err := oscorestore.NewFileStore(*stateDir, "go-coap-example-server")
	if err != nil {
		log.Fatalf("create OSCORE state store: %v", err)
	}
	securityContext, err := oscore.NewContext(oscore.ContextConfig{
		MasterSecret:        masterSecret,
		SenderID:            []byte("server"),
		RecipientID:         []byte("client"),
		SenderSequenceStore: state,
		ReplayWindowStore:   state,
	})
	if err != nil {
		log.Fatalf("create OSCORE context: %v", err)
	}
	processor, err := oscore.NewProcessor(oscore.ProcessorConfig{
		Contexts:      []*oscore.Context{securityContext},
		RequireOSCORE: true,
	})
	if err != nil {
		log.Fatalf("create OSCORE processor: %v", err)
	}

	handler := func(w *responsewriter.ResponseWriter[*client.Conn], req *pool.Message) {
		if req.Code() != codes.GET {
			if err := w.SetResponse(codes.MethodNotAllowed, message.TextPlain, nil); err != nil {
				log.Printf("set method-not-allowed response: %v", err)
			}
			return
		}
		if err := w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("Hello over OSCORE"))); err != nil {
			log.Printf("set OSCORE response: %v", err)
		}
	}
	server := udp.NewServer(
		options.WithOSCORE(processor),
		options.WithHandlerFunc(handler),
	)
	listener, err := coapNet.NewListenUDP("udp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	log.Printf("OSCORE CoAP server listening on %s", listener.LocalAddr())
	log.Fatal(server.Serve(listener))
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
