package coap

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/go-ocf/go-coap/codes"
)

func periodicTransmitter(w ResponseWriter, r *Request) {
	msg := r.Client.NewMessage(MessageParams{
		Type:      Acknowledgement,
		Code:      codes.Content,
		MessageID: r.Msg.MessageID(),
		Payload:   make([]byte, 15),
		Token:     r.Msg.Token(),
	})

	msg.SetOption(ContentFormat, TextPlain)
	msg.SetOption(LocationPath, r.Msg.Path())

	err := w.WriteMsg(msg)
	if err != nil {
		log.Printf("Error on transmitter, stopping: %v", err)
		return
	}

	go func() {
		time.Sleep(time.Second)
		err := w.WriteMsg(msg)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}
	}()
}

func testServingObservation(t *testing.T, net string, addrstr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx) {
	sync := make(chan bool)

	client := &Client{
		Handler: func(w ResponseWriter, r *Request) {
			log.Printf("Gotaaa %s", r.Msg.Payload())
			sync <- true
		},
		Net:                  net,
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
		MaxMessageSize:       ^uint32(0),
	}

	conn, err := client.Dial(addrstr)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}

	defer conn.Close()

	req := conn.NewMessage(MessageParams{
		Type:      NonConfirmable,
		Code:      codes.GET,
		MessageID: 12345,
		Token:     []byte{123},
	})

	req.AddOption(Observe, 1)
	req.SetPathString("/some/path")

	err = conn.WriteMsg(req)
	if err != nil {
		t.Fatalf("Error sending request: %v", err)
	}

	<-sync
	log.Printf("Done...\n")
}

func TestServingUDPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", false, BlockWiseSzx16, periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "udp", addrstr, false, BlockWiseSzx16)
}

func TestServingTCPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerTCPWithHandler(":0", false, BlockWiseSzx16, periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "tcp", addrstr, false, BlockWiseSzx16)
}

func setupServer(t *testing.T) (*Server, string, chan error, error) {
	return RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx1024, func(w ResponseWriter, r *Request) {
		msg := r.Client.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      codes.Content,
			MessageID: r.Msg.MessageID(),
			Payload:   make([]byte, 5000),
			Token:     r.Msg.Token(),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, r.Msg.Path())

		err := w.WriteMsg(msg)
		if err != nil {
			t.Fatalf("Error on transmitter, stopping: %v", err)
			return
		}
	})
}

func TestServingUDPGet(t *testing.T) {

	s, addr, fin, err := setupServer(t)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx16
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	_, err = con.Get("/tmp/test")
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPPost(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Post("/tmp/test", TextPlain, body)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPPut(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Put("/tmp/test", TextPlain, body)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPDelete(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	_, err = con.Delete("/tmp/test")
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPObserve(t *testing.T) {
	s, addr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx16, func(w ResponseWriter, r *Request) {
		msg := r.Client.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      codes.Content,
			MessageID: r.Msg.MessageID(),
			Payload:   make([]byte, 17),
			Token:     r.Msg.Token(),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, r.Msg.Path())
		msg.SetOption(Observe, 2)

		err := w.WriteMsg(msg)
		if err != nil {
			t.Fatalf("Error on transmitter, stopping: %v", err)
			return
		}
	})
	defer func() {
		s.Shutdown()
		<-fin
	}()
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	sync := make(chan bool)
	_, err = con.Observe("/tmp/test", func(req *Request) {
		sync <- true
	})
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	<-sync
}
