package main

import (
	"log"

	coap "github.com/go-ocf/go-coap"
)

func main() {
	client := &coap.MulticastClient{}

	conn, err := client.Dial("224.0.1.187:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}

	sync := make(chan bool)
	_, err = conn.Publish("/oic/res", func(req *coap.Request) {
		log.Printf("Got message: %#v from %v", req.Msg, req.Client.RemoteAddr())
		sync <- true
	})
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}
	<-sync
}
