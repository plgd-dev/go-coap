package coap

import (
	"bytes"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func periodicTransmitter(w ResponseWriter, r *Request) {
	msg := r.Client.NewMessage(MessageParams{
		Type:      Acknowledgement,
		Code:      Content,
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
	}

	conn, err := client.Dial(addrstr)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}

	defer conn.Close()

	req := conn.NewMessage(MessageParams{
		Type:      NonConfirmable,
		Code:      GET,
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

func testServingMCastByClient(t *testing.T, lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockWiseSzx, ifis []net.Interface) {
	payload := []byte("mcast payload")
	addrMcast := laddr
	ansArrived := make(chan bool)

	c := Client{
		Net: lnet,
		Handler: func(w ResponseWriter, r *Request) {
			if bytes.Equal(r.Msg.Payload(), payload) {
				log.Printf("mcast %v -> %v", r.Client.RemoteAddr(), r.Client.LocalAddr())
				ansArrived <- true
			} else {
				t.Fatalf("unknown payload %v arrived from %v", r.Msg.Payload(), r.Client.RemoteAddr())
			}
		},
		BlockWiseTransfer:    &BlockWiseTransfer,
		BlockWiseTransferSzx: &BlockWiseTransferSzx,
	}
	var a *net.UDPAddr
	var err error
	if a, err = net.ResolveUDPAddr(strings.TrimSuffix(lnet, "-mcast"), addrMcast); err != nil {
		t.Fatalf("cannot resolve addr: %v", err)
	}
	co, err := c.Dial(addrMcast)
	if err != nil {
		t.Fatalf("cannot dial addr: %v", err)
	}

	if err := joinGroup(co.srv.Conn.(*net.UDPConn), nil, a); err != nil {
		t.Fatalf("cannot join self to multicast group: %v", err)
	}
	if ip4 := co.srv.Conn.(*net.UDPConn).LocalAddr().(*net.UDPAddr).IP.To4(); ip4 != nil {
		if err := ipv4.NewPacketConn(co.srv.Conn.(*net.UDPConn)).SetMulticastLoopback(true); err != nil {
			t.Fatalf("cannot allow multicast loopback: %v", err)
		}
	} else {
		if err := ipv6.NewPacketConn(co.srv.Conn.(*net.UDPConn)).SetMulticastLoopback(true); err != nil {
			t.Fatalf("cannot allow multicast loopback: %v", err)
		}
	}
	if err != nil {
		t.Fatalf("unable to dialing: %v", err)
	}
	defer co.Close()

	req := &DgramMessage{
		MessageBase{
			typ:       NonConfirmable,
			code:      GET,
			messageID: 1234,
			payload:   payload,
		}}
	req.SetOption(ContentFormat, TextPlain)
	req.SetPathString("/test")

	co.WriteMsg(req)

	<-ansArrived
}

func TestServingIPv4MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", false, BlockWiseSzx16, []net.Interface{})
}

func TestServingIPv6MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", false, BlockWiseSzx16, []net.Interface{})
}

func TestServingIPv4AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", false, BlockWiseSzx16, ifis)
}

func TestServingIPv6AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", false, BlockWiseSzx16, ifis)
}

func setupServer(t *testing.T) (string, error) {
	_, addr, _, err := RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx1024, func(w ResponseWriter, r *Request) {
		msg := r.Client.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      Content,
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
	return addr, err
}

func TestServingUDPGet(t *testing.T) {

	addr, err := setupServer(t)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

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
	addr, err := setupServer(t)
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
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Post("/tmp/test", TextPlain, body)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPPut(t *testing.T) {
	addr, err := setupServer(t)
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
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Put("/tmp/test", TextPlain, body)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPDelete(t *testing.T) {
	addr, err := setupServer(t)
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
	_, err = con.Delete("/tmp/test")
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
}

func TestServingUDPObserve(t *testing.T) {
	_, addr, _, err := RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx16, func(w ResponseWriter, r *Request) {
		msg := r.Client.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      Content,
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
