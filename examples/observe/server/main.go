package main

import (
	"bytes"
	"fmt"
	"log"
	"time"

	coap "github.com/plgd-dev/go-coap/v2"
	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/mux"
)

func getPath(opts message.Options) string {
	path, err := opts.Path()
	if err != nil {
		log.Printf("cannot get path: %v", err)
		return ""
	}
	return path
}

func sendResponse(cc mux.Client, token []byte, subded time.Time, obs int64) error {
	m := cc.AcquireMessage(cc.Context())
	defer cc.ReleaseMessage(m)
	m.SetCode(codes.Content)
	m.SetToken(token)
	m.SetBody(bytes.NewReader([]byte(fmt.Sprintf("Been running for %v", time.Since(subded)))))
	m.SetContentFormat(message.TextPlain)
	if obs >= 0 {
		m.SetObserve(uint32(obs))
	}
	return cc.WriteMessage(m)
}

func periodicTransmitter(cc mux.Client, token []byte) {
	subded := time.Now()

	for obs := int64(2); ; obs++ {
		err := sendResponse(cc, token, subded, obs)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}
		time.Sleep(time.Second)
	}
}

func main() {
	log.Fatal(coap.ListenAndServe("udp", ":5688",
		mux.HandlerFunc(func(w mux.ResponseWriter, r *mux.Message) {
			log.Printf("Got message path=%v: %+v from %v", getPath(r.Options()), r, w.ClientConn().RemoteAddr())
			obs, err := r.Options().Observe()
			switch {
			case r.Code() == codes.GET && err == nil && obs == 0:
				go periodicTransmitter(w.ClientConn(), r.Token())
			case r.Code() == codes.GET:
				err := sendResponse(w.ClientConn(), r.Token(), time.Now(), -1)
				if err != nil {
					log.Printf("Error on transmitter: %v", err)
				}
			}
		})))
}
