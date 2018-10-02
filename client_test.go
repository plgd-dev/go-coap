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

func periodicTransmitter(w ResponseWriter, r *Request) {
	subded := time.Now()

	for {
		msg := r.SessionNet.NewMessage(MessageParams{
			Type:      Acknowledgement,
			Code:      Content,
			MessageID: r.Msg.MessageID(),
			Payload:   []byte(fmt.Sprintf("Been running for %v", time.Since(subded))),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, r.Msg.Path())

		err := w.Write(msg)
		if err != nil {
			log.Printf("Error on transmitter, stopping: %v", err)
			return
		}

		time.Sleep(time.Second)
	}
}

func testServingObservation(t *testing.T, net string, addrstr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockSzx) {
	sync := make(chan bool)

	client := &Client{
		ObserverFunc: func(w ResponseWriter, r *Request) {
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
	})

	req.AddOption(Observe, 1)
	req.SetPathString("/some/path")

	err = conn.Write(req)
	if err != nil {
		t.Fatalf("Error sending request: %v", err)
	}

	<-sync
	log.Printf("Done...\n")
}

func TestServingUDPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", false, BlockSzx16, periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "udp", addrstr, false, BlockSzx16)
}

func TestServingTCPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerTCPWithHandler(":0", false, BlockSzx16, periodicTransmitter)
	if err != nil {
		t.Fatalf("unable to run test server: %v", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "tcp", addrstr, false, BlockSzx16)
}

func testServingMCastByClient(t *testing.T, lnet, laddr string, BlockWiseTransfer bool, BlockWiseTransferSzx BlockSzx, ifis []net.Interface) {
	payload := []byte("mcast payload")
	addrMcast := laddr
	ansArrived := make(chan bool)

	c := Client{
		Net: lnet,
		ObserverFunc: func(w ResponseWriter, r *Request) {
			if bytes.Equal(r.Msg.Payload(), payload) {
				log.Printf("mcast %v -> %v", r.SessionNet.RemoteAddr(), r.SessionNet.LocalAddr())
				ansArrived <- true
			} else {
				t.Fatalf("unknown payload %v arrived from %v", r.Msg.Payload(), r.SessionNet.RemoteAddr())
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

	co.Write(req)

	<-ansArrived
}

func TestServingIPv4MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", false, BlockSzx16, []net.Interface{})
}

func TestServingIPv6MCastByClient(t *testing.T) {
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", false, BlockSzx16, []net.Interface{})
}

func TestServingIPv4AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp4-mcast", "225.0.1.187:11111", false, BlockSzx16, ifis)
}

func TestServingIPv6AllInterfacesMCastByClient(t *testing.T) {
	ifis, err := net.Interfaces()
	if err != nil {
		t.Fatalf("unable to get interfaces: %v", err)
	}
	testServingMCastByClient(t, "udp6-mcast", "[ff03::158]:11111", false, BlockSzx16, ifis)
}
