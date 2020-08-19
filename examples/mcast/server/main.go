package main

import (
	"bytes"
	"log"
	gonet "net"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/udp"
)

func handleMcast(w mux.ResponseWriter, r *mux.Message) {
	path, err := r.Options.Path()
	if err != nil {
		log.Printf("cannot get path: %v", err)
		return
	}

	log.Printf("Got mcast message: path=%q: from %v", path, w.Client().RemoteAddr())
	w.SetResponse(codes.Content, message.TextPlain, bytes.NewReader([]byte("mcast response")))
}

func main() {
	m := mux.NewRouter()
	m.Handle("/oic/res", mux.HandlerFunc(handleMcast))
	multicastAddr := "224.0.1.187:5683"

	l, err := net.NewListenUDP("udp4", multicastAddr)
	if err != nil {
		log.Println(err)
		return
	}

	ifaces, err := gonet.Interfaces()
	if err != nil {
		log.Println(err)
		return
	}

	a, err := gonet.ResolveUDPAddr("udp", multicastAddr)
	if err != nil {
		log.Println(err)
		return
	}

	for _, iface := range ifaces {
		err := l.JoinGroup(&iface, a)
		if err != nil {
			log.Printf("cannot JoinGroup(%v, %v): %v", iface, a, err)
		}
	}
	err = l.SetMulticastLoopback(true)
	if err != nil {
		log.Println(err)
		return
	}

	defer l.Close()
	s := udp.NewServer(udp.WithMux(m))
	defer s.Stop()
	log.Fatal(s.Serve(l))
}
