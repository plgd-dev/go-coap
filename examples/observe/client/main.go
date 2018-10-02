package main

import (
	"log"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func observe(s coap.SessionNet, m coap.Message) {
	log.Printf("Got %s", m.Payload())
}

func main() {
	client := &coap.Client{ObserverFunc: observe}

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

	err = conn.WriteMsg(req, time.Hour)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	// waiting for messages that arrives in 20seconds
	time.Sleep(20 * time.Second)
	log.Printf("Done...\n")

}
