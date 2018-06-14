package main

import (
	"fmt"
	"log"
	"time"

	"github.com/ondrejtomcik/go-coap"
)

func periodicTransmitter(w coap.Session, req coap.Message) {
	subded := time.Now()

	for {
		msg := w.NewMessage(coap.MessageParams{
			Type:      coap.Acknowledgement,
			Code:      coap.Content,
			MessageID: req.MessageID(),
			Payload:   []byte(fmt.Sprintf("Been running for %v", time.Since(subded))),
			Token:     []byte("123"),
		})

		msg.SetOption(coap.ContentFormat, coap.TextPlain)
		msg.SetOption(coap.LocationPath, req.Path())

		log.Printf("Transmitting %v", msg)
		err := w.WriteMsg(msg, time.Hour)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}

		time.Sleep(time.Second)
	}
}

func main() {
	log.Fatal(coap.ListenAndServe(":5688", "udp",
		coap.HandlerFunc(func(w coap.Session, req coap.Message) {
			log.Printf("Got message path=%q: %#v from %v", req.Path(), req, w.RemoteAddr())
			if req.Code() == coap.GET && req.Option(coap.Observe) != nil {
				value := req.Option(coap.Observe)
				if value.(uint32) > 0 {
					go periodicTransmitter(w, req)
				} else {
					log.Printf("coap.Observe value=%v", value)
				}
			}
		})))
}
