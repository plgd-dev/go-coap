package main

import (
	"bytes"
	"flag"
	"log"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
)

func newRouter() (*mux.Router, error) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	router := mux.NewRouter()
	err := router.Handle("/data", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		body, code := payload, codes.Content
		switch r.Code() {
		case codes.GET:
		case codes.PUT:
			var err error
			body, err = r.ReadBody()
			if err != nil {
				log.Printf("read upload: %v", err)
				if responseErr := w.SetResponse(codes.BadRequest, message.TextPlain, nil); responseErr != nil {
					log.Printf("set error response: %v", responseErr)
				}
				return
			}
			code = codes.Changed
		default:
			if err := w.SetResponse(codes.MethodNotAllowed, message.TextPlain, nil); err != nil {
				log.Printf("set method error response: %v", err)
			}
			return
		}
		if err := w.SetResponse(code, message.AppOctets, bytes.NewReader(body)); err != nil {
			log.Printf("set response: %v", err)
			return
		}
		log.Printf("%v /data: %d bytes", r.Code(), len(body))
	}))
	return router, err
}

func main() {
	addr := flag.String("addr", "127.0.0.1:5688", "UDP listen address")
	flag.Parse()
	router, err := newRouter()
	if err != nil {
		log.Fatal(err)
	}
	listener, err := coapNet.NewListenUDP("udp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	server := udp.NewServer(
		options.WithMux(router),
		options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
	)
	log.Printf("QBlockwise Auto server listening on %s", listener.LocalAddr())
	log.Fatal(server.Serve(listener))
}
