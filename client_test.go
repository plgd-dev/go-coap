package coap

import (
	"fmt"
	"log"
	"testing"
	"time"
)

func periodicTransmitter(w Session, req Message) {
	subded := time.Now()

	for {
		msg := w.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      Content,
			MessageID: req.MessageID(),
			Payload:   []byte(fmt.Sprintf("Been running for %v", time.Since(subded))),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, req.Path())

		err := w.WriteMsg(msg)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}

		time.Sleep(time.Second)
	}
}

func testServingObservation(t *testing.T, net string, addrstr string) {
	sync := make(chan bool)

	client := &Client{ObserveFunc: func(s Session, m Message) {
		log.Printf("Gotaaa %s", m.Payload())
		sync <- true
	}, Net: net}

	conn, err := client.Dial(addrstr)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}

	defer conn.Close()

	req := conn.NewMessage(MessageParams{
		Type:      NonConfirmable,
		Code:      GET,
		MessageID: 12345,
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
	s, addrstr, fin, err := RunLocalServerUDPWithHandler(":0", periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "udp", addrstr)
}

func TestServingTCPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerTCPWithHandler(":0", periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "tcp", addrstr)

}
