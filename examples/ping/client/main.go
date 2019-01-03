package main

import (
	"log"
	"os"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Run %v HOST:PORT ", os.Args[0])
	}

	co, err := coap.Dial("tcp", os.Args[1])
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	path := "/a"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	//to start ping in direction server to client
	resp, err := co.Get(path)

	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	log.Printf("Response payload: %v", resp.Payload())

	for {
		if err := co.Ping(time.Millisecond * 500); err != nil {
			log.Printf("Error occurs during ping server: %v", err)
			return
		}
		log.Printf("Pong received from server: %v", co.RemoteAddr())
		time.Sleep(time.Millisecond * 500)
	}
}
