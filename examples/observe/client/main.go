package main

import (
	"log"
	"time"

	"github.com/ondrejtomcik/go-coap"
)

func main() {
	conn, err := coap.Dial("udp", "localhost:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}

	req := conn.NewMessage(coap.MessageParams{
		Type:      coap.NonConfirmable,
		Code:      coap.GET,
		MessageID: 12345,
	})

	req.AddOption(coap.Observe, 1)
	req.SetPathString("/some/path")

	err = conn.WriteMsg(req, 1*time.Second)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	for err == nil {
		var rv coap.Message
		rv, err = conn.ReadMsg(2 * time.Second)
		if err != nil {
			log.Fatalf("Error receiving: %v", err)
		} else if rv != nil {
			log.Printf("Got %s", rv.Payload())
		}

	}
	log.Printf("Done...\n")

}
