package coap

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/go-ocf/go-coap/codes"
	"github.com/stretchr/testify/require"
)

func periodicTransmitter(w ResponseWriter, r *Request) {
	typ := NonConfirmable
	if r.Msg.Type() == Confirmable {
		typ = Acknowledgement
	}
	msg := r.Client.NewMessage(MessageParams{
		Type:      typ,
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
	require.NoError(t, err)

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
	require.NoError(t, err)

	<-sync
	log.Printf("Done...\n")
}

func TestServingUDPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", false, BlockWiseSzx16, periodicTransmitter)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "udp", addrstr, false, BlockWiseSzx16)
}

func TestServingTCPObservation(t *testing.T) {
	s, addrstr, fin, err := RunLocalServerTCPWithHandler(":0", false, BlockWiseSzx16, periodicTransmitter)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()
	testServingObservation(t, "tcp", addrstr, false, BlockWiseSzx16)
}

func setupServer(t *testing.T) (*Server, string, chan error, error) {
	return RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx1024, func(w ResponseWriter, r *Request) {
		typ := NonConfirmable
		if r.Msg.Type() == Confirmable {
			typ = Acknowledgement
		}
		msg := r.Client.NewMessage(MessageParams{
			Type:      typ,
			Code:      codes.Content,
			MessageID: r.Msg.MessageID(),
			Payload:   make([]byte, 5000),
			Token:     r.Msg.Token(),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, r.Msg.Path())

		err := w.WriteMsg(msg)
		require.NoError(t, err)
	})
}

func TestServingUDPGet(t *testing.T) {

	s, addr, fin, err := setupServer(t)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx16
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	require.NoError(t, err)
	_, err = con.Get("/tmp/test")
	require.NoError(t, err)
}

func TestServingUDPPost(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	require.NoError(t, err)
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Post("/tmp/test", TextPlain, body)
	require.NoError(t, err)
}

func TestServingUDPPut(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	require.NoError(t, err)
	body := bytes.NewReader([]byte("Hello world"))
	_, err = con.Put("/tmp/test", TextPlain, body)
	require.NoError(t, err)
}

func TestServingUDPDelete(t *testing.T) {
	s, addr, fin, err := setupServer(t)
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	require.NoError(t, err)
	_, err = con.Delete("/tmp/test")
	require.NoError(t, err)
}

func TestServingUDPObserve(t *testing.T) {
	s, addr, fin, err := RunLocalServerUDPWithHandler("udp", ":0", true, BlockWiseSzx16, func(w ResponseWriter, r *Request) {
		typ := NonConfirmable
		if r.Msg.Type() == Confirmable {
			typ = Acknowledgement
		}
		msg := r.Client.NewMessage(MessageParams{
			Type:      typ,
			Code:      codes.Content,
			MessageID: r.Msg.MessageID(),
			Payload:   make([]byte, 17),
			Token:     r.Msg.Token(),
		})

		msg.SetOption(ContentFormat, TextPlain)
		msg.SetOption(LocationPath, r.Msg.Path())
		msg.SetOption(Observe, 2)

		err := w.WriteMsg(msg)
		require.NoError(t, err)
	})
	defer func() {
		s.Shutdown()
		<-fin
	}()
	require.NoError(t, err)

	BlockWiseTransfer := true
	BlockWiseTransferSzx := BlockWiseSzx1024
	c := Client{Net: "udp", BlockWiseTransfer: &BlockWiseTransfer, BlockWiseTransferSzx: &BlockWiseTransferSzx}
	con, err := c.Dial(addr)
	require.NoError(t, err)
	sync := make(chan bool)
	_, err = con.Observe("/tmp/test", func(req *Request) {
		sync <- true
	})
	require.NoError(t, err)
	<-sync
}
