package main

import (
	"bytes"
	"fmt"
	"log"

	piondtls "github.com/pion/dtls/v2"
	coap "github.com/plgd-dev/go-coap/v2"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/mux"
)

func handleA(w mux.ResponseWriter, r *mux.Message) {
	log.Printf("got message in handleA:  %+v from %v\n", r, w.Client().RemoteAddr())
	err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("A hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func handleB(w mux.ResponseWriter, r *mux.Message) {
	log.Printf("got message in handleB:  %+v from %v\n", r, w.Client().RemoteAddr())
	customResp := message.Message{
		Code:    codes.Content,
		Token:   r.Token,
		Context: r.Context,
		Options: make(message.Options, 0, 16),
		Body:    bytes.NewReader([]byte("B hello world")),
	}
	optsBuf := make([]byte, 32)
	opts, used, err := customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
	if err == message.ErrTooSmall {
		optsBuf = append(optsBuf, make([]byte, used)...)
		opts, used, err = customResp.Options.SetContentFormat(optsBuf, message.TextPlain)
	}
	if err != nil {
		log.Printf("cannot set options to response: %v", err)
		return
	}
	optsBuf = optsBuf[:used]
	customResp.Options = opts

	err = w.Client().WriteMessage(&customResp)
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func main() {
	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(handleA))
	m.Handle("/b", mux.HandlerFunc(handleB))

	log.Fatal(coap.ListenAndServeDTLS("udp", ":5688", &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Client's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Client"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}, m))
}
