package main

import (
	"log"

	coap "github.com/go-ocf/go-coap"
)

func handleA(w coap.ResponseWriter, req *coap.Request) {
	log.Printf("Got message in handleA: path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
	if req.Msg.IsConfirmable() {
		res := req.Client.NewMessage(coap.MessageParams{
			Type:      coap.Acknowledgement,
			Code:      coap.Content,
			MessageID: req.Msg.MessageID(),
			Token:     req.Msg.Token(),
			Payload:   []byte("hello to you!"),
		})
		res.SetOption(coap.ContentFormat, coap.TextPlain)

		log.Printf("Transmitting from A %#v", res)
		w.Write(res)
	}
}

func handleB(w coap.ResponseWriter, req *coap.Request) {
	log.Printf("Got message in handleB: path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
	if req.Msg.IsConfirmable() {
		res := req.Client.NewMessage(coap.MessageParams{
			Type:      coap.Acknowledgement,
			Code:      coap.Content,
			MessageID: req.Msg.MessageID(),
			Token:     req.Msg.Token(),
			Payload:   []byte("good bye!"),
		})
		res.SetOption(coap.ContentFormat, coap.TextPlain)

		log.Printf("Transmitting from B %#v", res)
		w.Write(res)
	}
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/a", coap.HandlerFunc(handleA))
	mux.Handle("/b", coap.HandlerFunc(handleB))

	log.Fatal(coap.ListenAndServe(":5688", "udp", mux))
}
