package main

import (
	"log"

	coap "github.com/go-ocf/go-coap"
)

func handleMcast(w coap.ResponseWriter, r *coap.Request) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", r.Msg.Path(), r.Msg, r.Client.RemoteAddr())
	w.SetContentFormat(coap.TextPlain)
	if _, err := w.Write([]byte("hello to you!")); err != nil {
		log.Printf("Cannot write resp %v", err)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/oic/res", coap.HandlerFunc(handleMcast))

	log.Fatal(coap.ListenAndServe("224.0.1.187:5688", "udp-mcast", mux))
}
