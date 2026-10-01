package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:5688", "UDP listen address")
	responseBytes := flag.Int("response-bytes", 32<<10, "GET response body size")
	flag.Parse()
	maxBody := int(qblock.DefaultManagerConfig().Transfer.MaxBodySize)
	if *responseBytes < 0 || *responseBytes > maxBody {
		return fmt.Errorf("response size must be between 0 and %d bytes", maxBody)
	}

	router := mux.NewRouter()
	router.Handle("/qblock", mux.HandlerFunc(func(w mux.ResponseWriter, req *mux.Message) {
		var body []byte
		var err error
		responseCode := codes.Content
		switch req.Code() {
		case codes.GET:
			body = patternedBody(*responseBytes)
		case codes.POST, codes.PUT:
			responseCode = codes.Changed
			if req.Body() != nil {
				body, err = io.ReadAll(req.Body())
			}
			if err != nil {
				log.Printf("read assembled request: %v", err)
				_ = w.SetResponse(codes.BadRequest, message.TextPlain, bytes.NewReader([]byte("invalid body")))
				return
			}
		default:
			_ = w.SetResponse(codes.MethodNotAllowed, message.TextPlain, nil)
			return
		}

		log.Printf("assembled request: peer=%v method=%s bytes=%d", w.Conn().RemoteAddr(), req.Code(), len(body))
		if err := w.SetResponse(responseCode, message.AppOctets, bytes.NewReader(body)); err != nil {
			log.Printf("write complete response: %v", err)
		}
	}))

	listener, err := coapNet.NewListenUDP("udp4", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	defer listener.Close()

	srv := udp.NewServer(
		options.WithMux(router),
		options.WithQBlockServer(qblock.DefaultServerConfig()),
	)
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(listener) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	log.Printf("Q-Block UDP server listening on %s", listener.LocalAddr())
	select {
	case err := <-serveDone:
		return err
	case <-ctx.Done():
	}

	srv.Stop()
	if err := <-serveDone; err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func patternedBody(size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte((i*31 + 7) & 0xff)
	}
	return body
}
