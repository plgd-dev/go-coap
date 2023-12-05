package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"

	piondtls "github.com/pion/dtls/v2"
	"github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/options"
	udpClient "github.com/plgd-dev/go-coap/v3/udp/client"
)

func handleA(w mux.ResponseWriter, r *mux.Message) {
	log.Printf("got message in handleA:  %+v from %v\n", r, w.Conn().RemoteAddr())
	err := w.SetResponse(codes.GET, message.TextPlain, bytes.NewReader([]byte("A hello world")))
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

func handleB(w mux.ResponseWriter, r *mux.Message) {
	log.Printf("got message in handleB:  %+v from %v\n", r, w.Conn().RemoteAddr())
	customResp := w.Conn().AcquireMessage(r.Context())
	defer w.Conn().ReleaseMessage(customResp)
	customResp.SetCode(codes.Content)
	customResp.SetToken(r.Token())
	customResp.SetContentFormat(message.TextPlain)
	customResp.SetBody(bytes.NewReader([]byte("B hello world")))
	err := w.Conn().WriteMessage(customResp)
	if err != nil {
		log.Printf("cannot set response: %v", err)
	}
}

// wrappedListener wraps a net.Listener and implements a go-coap DTLS
// server.Listener.
// NOTE: this utility is for example purposes only. Context should be handled
// properly in meaningful scenarios.
type wrappedListener struct {
	l      net.Listener
	closed atomic.Bool
}

// AcceptWithContext disregards the passed context and calls the underlying
// net.Listener Accept().
func (w *wrappedListener) AcceptWithContext(_ context.Context) (net.Conn, error) {
	return w.l.Accept()
}

// Close calls the underlying net.Listener Close().
func (w *wrappedListener) Close() error {
	return w.l.Close()
}

// wrapListener wraps a net.Listener and returns a DTLS server.Listener.
func wrapListener(l net.Listener) server.Listener {
	return &wrappedListener{
		l: l,
	}
}

func main() {
	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(handleA))
	m.Handle("/b", mux.HandlerFunc(handleB))
	laddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:5688")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	l, err := piondtls.Listen("udp", laddr, &piondtls.Config{
		PSK: func(hint []byte) ([]byte, error) {
			fmt.Printf("Client's hint: %s \n", hint)
			return []byte{0xAB, 0xC1, 0x23}, nil
		},
		PSKIdentityHint: []byte("Pion DTLS Server"),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	})
	if err != nil {
		log.Fatalf("Error establishing DTLS listener: %v", err)
	}
	s := server.New(options.WithMux(m), options.WithInactivityMonitor(10*time.Second, func(cc *udpClient.Conn) {}))
	s.Serve(wrapListener(l))
}
