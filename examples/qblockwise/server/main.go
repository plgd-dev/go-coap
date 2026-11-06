package main

import (
	"bytes"
	"flag"
	"log"
	"sync"
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
	objectData := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	var payloadMu sync.Mutex
	router := mux.NewRouter()
	err := router.Handle("/data", mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
		var body []byte
		code := codes.Content
		switch r.Code() {
		case codes.GET:
			payloadMu.Lock()
			body = append([]byte(nil), objectData...)
			payloadMu.Unlock()
		case codes.FETCH:
			if _, err := r.ReadBody(); err != nil {
				log.Printf("read FETCH query: %v", err)
				if responseErr := w.SetResponse(codes.BadRequest, message.TextPlain, nil); responseErr != nil {
					log.Printf("set error response: %v", responseErr)
				}
				return
			}
			payloadMu.Lock()
			body = append([]byte(nil), objectData...)
			payloadMu.Unlock()
		case codes.PUT:
			var err error
			body, err = r.ReadBody()
			if err != nil {
				log.Printf("read %v body: %v", r.Code(), err)
				if responseErr := w.SetResponse(codes.BadRequest, message.TextPlain, nil); responseErr != nil {
					log.Printf("set error response: %v", responseErr)
				}
				return
			}
			code = codes.Changed
		case codes.PATCH, codes.IPATCH:
			var err error
			body, err = r.ReadBody()
			if err != nil {
				log.Printf("read %v body: %v", r.Code(), err)
				if responseErr := w.SetResponse(codes.BadRequest, message.TextPlain, nil); responseErr != nil {
					log.Printf("set error response: %v", responseErr)
				}
				return
			}
			code = codes.Changed
			payloadMu.Lock()
			switch r.Code() {
			case codes.PATCH:
				objectData = append(objectData, body...)
			case codes.IPATCH:
				objectData = append(objectData[:0], body...)
			}
			body = append([]byte(nil), objectData...)
			payloadMu.Unlock()
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
