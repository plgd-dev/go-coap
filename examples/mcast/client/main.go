package main

import (
	"context"
	"log"
	"time"

	"github.com/go-ocf/go-coap/v2/net"
	"github.com/go-ocf/go-coap/v2/udp"
	"github.com/go-ocf/go-coap/v2/udp/client"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"
)

func main() {
	l, err := net.NewListenUDP("udp4", "")
	if err != nil {
		log.Println(err)
		return
	}
	defer l.Close()
	s := udp.NewServer()
	defer s.Stop()
	go func() {
		s.Serve(l)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = s.Discover(ctx, "224.0.1.187:5683", "/oic/res", func(cc *client.ClientConn, resp *pool.Message) {
		log.Printf("discovered %v: %+v", cc.RemoteAddr(), resp.Message)
	})
	if err != nil {
		log.Println(err)
	}
}
