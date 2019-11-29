package main

import (
	"context"
	"log"
	"time"

	coap "github.com/go-ocf/go-coap"
	"github.com/go-ocf/go-coap/codes"
)

func handleA(w coap.ResponseWriter, req *coap.Request) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
	w.SetContentFormat(coap.TextPlain)
	log.Printf("Transmitting from A")
	ctx, cancel := context.WithTimeout(req.Ctx, time.Second)
	defer cancel()
	if _, err := w.WriteWithContext(ctx, []byte("hello world")); err != nil {
		log.Printf("Cannot send response: %v", err)
	}
}

func handleB(w coap.ResponseWriter, req *coap.Request) {
	log.Printf("Got message in handleB: path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
	resp := w.NewResponse(codes.Content)
	resp.SetOption(coap.ContentFormat, coap.TextPlain)
	resp.SetPayload([]byte("good bye!"))
	log.Printf("Transmitting from B %#v", resp)
	ctx, cancel := context.WithTimeout(req.Ctx, time.Second)
	defer cancel()
	if err := w.WriteMsgWithContext(ctx, resp); err != nil {
		log.Printf("Cannot send response: %v", err)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/a", coap.HandlerFunc(handleA))
	mux.Handle("/b", coap.HandlerFunc(handleB))

	log.Fatal(coap.ListenAndServe("udp", ":5688", mux))
}
