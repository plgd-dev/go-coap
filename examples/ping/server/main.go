package main

import (
	"log"
	"os"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func handleA(w coap.ResponseWriter, req *coap.Request) {
	log.Printf("Starting ping to client %v", req.Client.RemoteAddr())
	w.SetContentFormat(coap.TextPlain)
	if _, err := w.Write([]byte("hello world")); err != nil {
		log.Printf("Cannot send response: %v", err)
	}

	go func() {
		for {
			if err := req.Client.Ping(time.Millisecond * 500); err != nil {
				log.Printf("Error occurs during ping client: %v", err)
				return
			}
			log.Printf("Pong received from client: %v", req.Client.RemoteAddr())
			time.Sleep(time.Millisecond * 500)
		}
	}()
}

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Run %v LISTEN_ADDRESS:PORT ", os.Args[0])
	}

	log.Fatal(coap.ListenAndServe(os.Args[1], "tcp", coap.HandlerFunc(handleA)))
}
