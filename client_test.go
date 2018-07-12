package coap

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
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

		err := w.WriteMsg(msg, coapTimeout)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}

		time.Sleep(time.Second)
	}
}

func testServingObservation(t *testing.T, net string, addrstr string) {
	sync := make(chan bool)

	client := &Client{ObserverFunc: func(s Session, m Message) {
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

	err = conn.WriteMsg(req, coapTimeout)
	if err != nil {
		t.Fatalf("Error sending request: %v", err)
	}

	<-sync
	log.Printf("Done...\n")
}

func TestServingUDPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", periodicTransmitter)
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

func testServingMCastByClient(t *testing.T, lnet, laddr string, ifis []net.Interface) {
	payload := []byte("mcast payload")
	addrMcast := laddr
	ansArrived := make(chan bool)

	c := Client{Net: lnet, ObserverFunc: func(s Session, m Message) {
		if bytes.Equal(m.Payload(), payload) {
			log.Printf("mcast %v -> %v", s.RemoteAddr(), s.LocalAddr())
			ansArrived <- true
		} else {
			t.Fatalf("unknown payload %v arrived from %v", m.Payload(), s.RemoteAddr())
		}
	}}
	var a *net.UDPAddr
	var err error
	if a, err = net.ResolveUDPAddr(strings.TrimSuffix(lnet, "-mcast"), addrMcast); err != nil {
		t.Fatalf("cannot resolve addr: %v", err)
	}
	co, err := c.Dial(addrMcast)
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

	co.WriteMsg(req, time.Second)

	<-ansArrived
}

func TestServingIPv4MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", []net.Interface{})
}

func TestServingIPv6MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", []net.Interface{})
}

func TestServingIPv4AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", ifis)
}

func TestServingIPv6AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", ifis)
}
