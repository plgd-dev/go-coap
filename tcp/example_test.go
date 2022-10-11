package tcp_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/tcp"
)

func ExampleConn_Get() {
	conn, err := tcp.Dial("try.plgd.cloud:5683")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := conn.Get(ctx, "/oic/res")
	if err != nil {
		log.Fatal(err)
	}
	data, err := io.ReadAll(res.Body())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%v", data)
}

func ExampleServer() {
	l, err := net.NewTCPListener("tcp", "0.0.0.0:5683")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	s := tcp.NewServer()
	defer s.Stop()
	log.Fatal(s.Serve(l))
}
