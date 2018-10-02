package main

import (
	"log"
	"time"

	coap "github.com/go-ocf/go-coap"
)

func mcastResp(s coap.SessionNet, m coap.Message) {
	log.Printf("Got message: %#v from %v", m, s.RemoteAddr())
}

func main() {
	client := &coap.Client{Net: "udp-mcast", ObserverFunc: mcastResp}

	conn, err := client.Dial("224.0.1.187:5683")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}

	req := conn.NewMessage(coap.MessageParams{
		Type:      coap.NonConfirmable,
		Code:      coap.GET,
		MessageID: 12345,
		Payload:   []byte("mcast payload"),
	})
	req.SetOption(coap.ContentFormat, coap.TextPlain)
	req.SetPathString("/oic/res")

	err = conn.WriteMsg(req, time.Hour)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}

	// waiting for messages that arrives in 20seconds
	time.Sleep(20 * time.Second)
	log.Printf("Done...\n")

}
