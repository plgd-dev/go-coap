package main

import (
	"bytes"
	"fmt"
	"log"

	coap "github.com/go-ocf/go-coap"
	"github.com/ugorji/go/codec"
)

func decodeMsgToDebug(resp coap.Message, tag string) {
	var m interface{}
	err := codec.NewDecoderBytes(resp.Payload(), new(codec.CborHandle)).Decode(&m)
	out := fmt.Sprintf("RAW:\n%v\n", resp.Payload())
	if err == nil {
		bw := new(bytes.Buffer)
		h := new(codec.JsonHandle)
		h.BasicHandle.Canonical = true
		enc := codec.NewEncoder(bw, h)
		err = enc.Encode(m)
		if err != nil {
			log.Printf("Cannot encode %v to JSON: %v", m, err)
		} else {
			out = fmt.Sprintf("JSON:\n%v\n", bw.String())
		}
	}

	log.Print(
		"\n-------------------", tag, "------------------\n",
		"Path: ", resp.PathString(), "\n",
		"Code: ", resp.Code(), "\n",
		"Type: ", resp.Type(), "\n",
		"Query: ", resp.Options(coap.URIQuery), "\n",
		"ContentFormat: ", resp.Options(coap.ContentFormat), "\n",
		out,
	)
}

func main() {
	client := &coap.MulticastClient{}

	conn, err := client.Dial("224.0.1.187:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}

	sync := make(chan bool)
	req, err := conn.NewGetRequest("/oic/res")
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}
	req.SetOption(coap.URIQuery, "rt=oic.wk.d")
	_, err = conn.PublishMsg(req, func(req *coap.Request) {
		decodeMsgToDebug(req.Msg, "MCAST")
		sync <- true
	})
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}
	<-sync
}
