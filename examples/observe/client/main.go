package main

import (
	"log"
	"time"

	"github.com/ondrejtomcik/go-coap"
)

func observe(s coap.Session, m coap.Message) {
	log.Printf("Got %s", m.Payload())
}

func main() {
	client := &coap.Client{ObserveFunc: observe}

	conn, err := client.Dial("localhost:5688")
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

	err = conn.WriteMsg(req)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	time.Sleep(20 * time.Second)
	log.Printf("Done...\n")

}
