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
	m := message.Message{
		Code:    codes.Content,
		Token:   token,
		Context: cc.Context(),
		Body:    bytes.NewReader([]byte(fmt.Sprintf("Been running for %v", time.Since(subded)))),
	}
	var opts message.Options
	var buf []byte
	opts, n, err := opts.SetContentFormat(buf, message.TextPlain)
	if err == message.ErrTooSmall {
		buf = append(buf, make([]byte, n)...)
		opts, n, err = opts.SetContentFormat(buf, message.TextPlain)
	}
	if err != nil {
		return fmt.Errorf("cannot set content format to response: %w", err)
	}
	if obs >= 0 {
		opts, n, err = opts.SetObserve(buf, uint32(obs))
		if err == message.ErrTooSmall {
			buf = append(buf, make([]byte, n)...)
			opts, n, err = opts.SetObserve(buf, uint32(obs))
		}
		if err != nil {
			return fmt.Errorf("cannot set options to response: %w", err)
		}
	}
	m.Options = opts
	return cc.WriteMessage(&m)
}

func periodicTransmitter(cc mux.Client, token []byte) {
	subded := time.Now()
	obs := int64(2)
	for {
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
			log.Printf("Got message path=%v: %+v from %v", getPath(r.Options), r, w.Client().RemoteAddr())
			obs, err := r.Options.Observe()
			switch {
			case r.Code == codes.GET && err == nil && obs == 0:
				go periodicTransmitter(w.Client(), r.Token)
			case r.Code == codes.GET:
				subded := time.Now()
				err := sendResponse(w.Client(), r.Token, subded, -1)
				if err != nil {
					log.Printf("Error on transmitter: %v", err)
				}
			}
		})))
}
