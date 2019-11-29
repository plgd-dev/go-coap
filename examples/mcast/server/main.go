package main

import (
	"log"
	"sync"

	coap "github.com/go-ocf/go-coap"
	"github.com/go-ocf/go-coap/codes"
)

var responseClientConn = make(map[string]*coap.ClientConn, 0)
var lockResponseClientConn sync.Mutex

func handleDirect(w coap.ResponseWriter, r *coap.Request) {
	log.Printf("Got direct message: path=%q: from %v", r.Msg.Path(), r.Client.RemoteAddr())
	w.SetContentFormat(coap.TextPlain)
	if _, err := w.Write([]byte("direct response")); err != nil {
		log.Printf("Cannot write direct response %v", err)
		lockResponseClientConn.Lock()
		defer lockResponseClientConn.Unlock()
		delete(responseClientConn, r.Client.RemoteAddr().String())
	}
}

func handleMcast(w coap.ResponseWriter, r *coap.Request) {
	log.Printf("Got mcast message: path=%q: from %v", r.Msg.Path(), r.Client.RemoteAddr())

	remoteAddress := r.Client.RemoteAddr().String()
	resp := w.NewResponse(codes.Content)
	resp.SetOption(coap.ContentFormat, coap.TextPlain)
	resp.SetPayload([]byte("mcast response"))

	lockResponseClientConn.Lock()
	defer lockResponseClientConn.Unlock()
	// check if connection was not already exist
	if clientConn, ok := responseClientConn[remoteAddress]; ok {
		// send response via connection
		if err := clientConn.WriteMsg(resp); err != nil {
			log.Printf("cannot write response %v", err)
			delete(responseClientConn, remoteAddress)
		}
		return
	}

	// create connection and send response
	client := coap.Client{
		Handler: handleDirect,
	}
	clientConn, err := client.Dial(remoteAddress)
	if err != nil {
		log.Printf("cannot connect to %v: %v", remoteAddress, err)
	}
	err = clientConn.WriteMsg(resp)
	if err != nil {
		log.Printf("cannot write response %v", err)
		return
	}
	responseClientConn[remoteAddress] = clientConn
}

func main() {
	mux := coap.NewServeMux()
	mux.Handle("/oic/res", coap.HandlerFunc(handleMcast))

	log.Fatal(coap.ListenAndServe("udp-mcast", "224.0.1.187:5688", mux))
}
