package main

import (
	"bytes"
	"fmt"
	"log"
	"time"

	coap "github.com/go-ocf/go-coap/v2"
	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/mux"
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
		opts, n, err = opts.SetUint32(buf, message.Observe, uint32(obs))
		if err == message.ErrTooSmall {
			buf = append(buf, make([]byte, n)...)
			opts, n, err = opts.SetUint32(buf, message.Observe, uint32(obs))
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
		mux.HandlerFunc(func(w mux.ResponseWriter, req *message.Message) {
			log.Printf("Got message path=%v: %+v from %v", getPath(req.Options), req, w.Client().RemoteAddr())
			obs, err := req.Options.GetUint32(message.Observe)
			switch {
			case req.Code == codes.GET && err == nil && obs == 0:
				go periodicTransmitter(w.Client(), req.Token)
			case req.Code == codes.GET:
				subded := time.Now()
				err := sendResponse(w.Client(), req.Token, subded, -1)
				if err != nil {
					log.Printf("Error on transmitter: %v", err)
				}
			}
		})))
}
