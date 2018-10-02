package main

import (
	"log"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func handleMcast(w coap.SessionNet, req coap.Message) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
	res := w.NewMessage(coap.MessageParams{
		Type:      coap.Acknowledgement,
		Code:      coap.Content,
		MessageID: req.MessageID(),
		Token:     req.Token(),
		Payload:   []byte("hello to you!"),
	})
	res.SetOption(coap.ContentFormat, coap.TextPlain)

	if err := w.WriteMsg(res, time.Hour); err != nil {
		log.Printf("Cannot write resp %v", err)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/oic/res", coap.HandlerFunc(handleMcast))

	log.Fatal(coap.ListenAndServe("224.0.1.187:5683", "udp-mcast", mux))
}
