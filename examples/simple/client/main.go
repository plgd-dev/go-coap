package main

import (
	"log"
	"os"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func main() {
	co, err := coap.Dial("udp", "localhost:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}

	req := co.NewMessage(coap.MessageParams{
		Type:      coap.Confirmable,
		Code:      coap.GET,
		MessageID: 12345,
		Payload:   []byte("hello, world!"),
		Token:     []byte("1234"),
	})

	path := "/a"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	req.SetOption(coap.ETag, "weetag")
	req.SetOption(coap.MaxAge, 3)
	req.SetPathString(path)

	rv, err := co.Exchange(req, 1*time.Second)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	if rv != nil {
		log.Printf("Response payload: %v", rv.Payload())
	}

}
