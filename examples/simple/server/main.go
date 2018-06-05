package main

import (
	"log"

	"github.com/ondrejtomcik/go-coap"
)

func handleA(w coap.Session, req coap.Message) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
	if req.IsConfirmable() {
		res := w.NewMessage(coap.MessageParams{
			Type:      coap.Acknowledgement,
			Code:      coap.Content,
			MessageID: req.MessageID(),
			Token:     req.Token(),
			Payload:   []byte("hello to you!"),
		})
		res.SetOption(coap.ContentFormat, coap.TextPlain)

		log.Printf("Transmitting from A %#v", res)
		w.WriteMsg(res)
	}
}

func handleB(w coap.Session, req coap.Message) {
	log.Printf("Got message in handleB: path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
	if req.IsConfirmable() {
		res := w.NewMessage(coap.MessageParams{
			Type:      coap.Acknowledgement,
			Code:      coap.Content,
			MessageID: req.MessageID(),
			Token:     req.Token(),
			Payload:   []byte("good bye!"),
		})
		res.SetOption(coap.ContentFormat, coap.TextPlain)

		log.Printf("Transmitting from B %#v", res)
		w.WriteMsg(res)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/a", coap.HandlerFunc(handleA))
	mux.Handle("/b", coap.HandlerFunc(handleB))

	log.Fatal(coap.ListenAndServe(":5688", "udp", mux))
}
