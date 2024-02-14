package udp_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

func ExampleConn_Get() {
	conn, err := udp.Dial("pluggedin.cloud:5683")
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

func ExampleServer_Serve() {
	l, err := net.NewListenUDP("udp", "0.0.0.0:5683")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	s := udp.NewServer()
	defer s.Stop()
	log.Fatal(s.Serve(l))
}

func ExampleServer_Discover() {
	l, err := net.NewListenUDP("udp", "")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := udp.NewServer()
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		errS := s.Serve(l)
		if errS != nil {
			log.Println(errS)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = s.Discover(ctx, "224.0.1.187:5683", "/oic/res", func(_ *client.Conn, res *pool.Message) {
		data, errR := io.ReadAll(res.Body())
		if errR != nil {
			log.Fatal(errR)
		}
		fmt.Printf("%v", data)
	})
	if err != nil {
		log.Fatal(err)
	}
}
