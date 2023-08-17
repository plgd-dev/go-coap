package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"

	piondtls "github.com/pion/dtls/v2"
	coap "github.com/plgd-dev/go-coap/v3"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	"github.com/plgd-dev/go-coap/v3/options"

	dtlsServer "github.com/plgd-dev/go-coap/v3/dtls/server"
	tcpServer "github.com/plgd-dev/go-coap/v3/tcp/server"
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

func handleOnNewConn(cc *udpClient.Conn) {
	dtlsConn, ok := cc.NetConn().(*piondtls.Conn)
	if !ok {
		log.Fatalf("invalid type %T", cc.NetConn())
	}
	clientId := dtlsConn.ConnectionState().IdentityHint
	cc.SetContextValue("clientId", clientId)
	cc.AddOnClose(func() {
		clientId := dtlsConn.ConnectionState().IdentityHint
		log.Printf("closed connection clientId: %s", clientId)
	})
}

func main() {
	m := mux.NewRouter()
	m.Handle("/a", mux.HandlerFunc(handleA))
	m.Handle("/b", mux.HandlerFunc(handleB))

	tcpOpts := []tcpServer.Option{}
	tcpOpts = append(tcpOpts,
		options.WithMux(m),
		options.WithContext(context.Background()))

	dtlsOpts := []dtlsServer.Option{}
	dtlsOpts = append(dtlsOpts,
		options.WithMux(m),
		options.WithContext(context.Background()),
		options.WithOnNewConn(handleOnNewConn),
	)

	go func() {
		// serve a tcp server on :5686
		log.Fatal(coap.ListenAndServeWithOptions("tcp", ":5686", tcpOpts))
	}()

	go func() {
		// serve a tls tcp server on :5687
		log.Fatal(coap.ListenAndServeTCPTLSWithOptions("tcp", "5687", &tls.Config{}, tcpOpts...))
	}()

	go func() {
		// serve a udp dtls server on :5688
		log.Fatal(coap.ListenAndServeDTLSWithOptions("udp", ":5688", &piondtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				fmt.Printf("Client's hint: %s \n", hint)
				return []byte{0xAB, 0xC1, 0x23}, nil
			},
			PSKIdentityHint: []byte("Pion DTLS Client"),
			CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
		}, dtlsOpts...))
	}()
}
