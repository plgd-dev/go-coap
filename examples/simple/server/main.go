package main

import (
	"bytes"
	"log"

	coap "github.com/go-ocf/go-coap/v2"
	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/mux"
)

func handleA(w mux.ResponseWriter, req *message.Message) {
	log.Printf("got message in handleA:  %+v from %v\n", req, w.ClientConn().RemoteAddr())
	err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func handleB(w mux.ResponseWriter, req *message.Message) {
	log.Printf("got message in handleB:  %+v from %v\n", req, w.ClientConn().RemoteAddr())
	customResp := message.Message{
		Code:    codes.Content,
		Token:   req.Token,
		Context: req.Context,
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

	err = w.ClientConn().WriteMessage(&customResp)
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func main() {
	m := mux.NewServeMux()
	m.Handle("/a", mux.HandlerFunc(handleA))
	m.Handle("/b", mux.HandlerFunc(handleB))

	log.Fatal(coap.ListenAndServe("udp", ":5688", m))
}
