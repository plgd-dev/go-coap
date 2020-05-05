package main

import (
	"context"
	"log"
	"time"

	"github.com/go-ocf/go-coap/v2/udp"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

func main() {
	sync := make(chan bool)
	co, err := udp.Dial("localhost:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	num := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	obs, err := co.Observe(ctx, "/some/path", func(req *pool.Message) {
		log.Printf("Got %+v\n", req)
		num++
		if num >= 10 {
			sync <- true
		}
	})
	if err != nil {
		log.Fatalf("Unexpected error '%v'", err)
	}
	<-sync
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	obs.Cancel(ctx)
}
