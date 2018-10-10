package main

import (
	"log"

	coap "github.com/go-ocf/go-coap"
)

func handleMcast(w coap.ResponseWriter, r *coap.Request) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", r.Msg.Path(), r.Msg, r.Client.RemoteAddr())
	resp := w.NewResponse(coap.Content)
	resp.SetOption(coap.ContentFormat, coap.TextPlain)
	resp.SetPayload([]byte("hello to you!"))
	if err := w.Write(resp); err != nil {
		log.Printf("Cannot write resp %v", err)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/oic/res", coap.HandlerFunc(handleMcast))

	log.Fatal(coap.ListenAndServe("224.0.1.187:5683", "udp-mcast", mux))
}
