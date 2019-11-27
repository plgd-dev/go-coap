package main

import (
	"fmt"
	"log"
	"time"

	coap "github.com/go-ocf/go-coap"
	"github.com/go-ocf/go-coap/codes"
)

func sendResponse(w coap.ResponseWriter, req *coap.Request, subded time.Time) error {
	resp := w.NewResponse(codes.Content)
	resp.SetOption(coap.ContentFormat, coap.TextPlain)
	resp.SetPayload([]byte(fmt.Sprintf("Been running for %v", time.Since(subded))))
	return w.WriteMsg(resp)
}

func periodicTransmitter(w coap.ResponseWriter, req *coap.Request) {
	subded := time.Now()
	for {
		err := sendResponse(w, req, subded)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}
		time.Sleep(time.Second)
	}
}

func main() {
	log.Fatal(coap.ListenAndServe("udp", ":5688",
		coap.HandlerFunc(func(w coap.ResponseWriter, req *coap.Request) {
			log.Printf("Got message path=%q: %#v from %v", req.Msg.Path(), req.Msg, req.Client.RemoteAddr())
			switch {
			case req.Msg.Code() == codes.GET && req.Msg.Option(coap.Observe) != nil && req.Msg.Option(coap.Observe).(uint32) == 0:
				go periodicTransmitter(w, req)
			case req.Msg.Code() == codes.GET:
				subded := time.Now()
				err := sendResponse(w, req, subded)
				if err != nil {
					log.Printf("Error on transmitter: %v", err)
				}
			}
		})))
}
