// Command fixture runs one public go-coap role against an independent peer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	piondtls "github.com/pion/dtls/v3"
	"github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	coapNet "github.com/plgd-dev/go-coap/v3/net"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/net/qblock"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

func main() {
	role := flag.String("role", "client", "client or server")
	addr := flag.String("addr", "127.0.0.1:5683", "peer or listen address")
	method := flag.String("method", "GET", "GET, POST or PUT")
	path := flag.String("path", "/example_data", "request path")
	input := flag.String("input", "", "upload or server response file")
	output := flag.String("output", "", "response body file")
	transport := flag.String("transport", "udp", "udp or dtls (local test PSK)")
	probe := flag.String("probe", "/example_data", "explicit capability probe path")
	flag.Parse()
	if err := run(*role, *addr, *method, *path, *input, *output, *transport, *probe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pskConfig() *piondtls.Config {
	return &piondtls.Config{PSK: func([]byte) ([]byte, error) { return []byte("qblock-interop-local-key"), nil }, PSKIdentityHint: []byte("qblock-interop"), CipherSuites: []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_GCM_SHA256}}
}

func run(role, addr, method, path, input, output, transport, probe string) error {
	body, err := os.ReadFile(input)
	if input != "" && err != nil {
		return err
	}
	errors := options.WithErrors(func(err error) { fmt.Fprintln(os.Stderr, "transport:", err) })
	if role == "server" {
		cfg := qblock.DefaultServerConfig()
		cfg.ProbingRate = 65536
		handler := options.WithHandlerFunc(func(w *responsewriter.ResponseWriter[*client.Conn], r *pool.Message) {
			payload := body
			code := codes.Content
			if r.Code() == codes.POST || r.Code() == codes.PUT {
				var readErr error
				payload, readErr = r.ReadBody()
				if readErr != nil {
					fmt.Fprintln(os.Stderr, readErr)
					return
				}
				code = codes.Changed
			}
			p, _ := r.Path()
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "handler", "method": r.Code().String(), "path": p, "body_bytes": len(payload)})
			if err := w.SetResponse(code, message.TextPlain, bytes.NewReader(payload)); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		})
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if transport == "dtls" {
			s := dtls.NewServer(options.WithQBlockServer(cfg), errors, options.WithBlockwise(false, blockwise.SZX64, 10*time.Second), handler)
			l, err := coapNet.NewDTLSListener("udp4", addr, pskConfig())
			if err != nil {
				return err
			}
			go func() { <-ctx.Done(); s.Stop() }()
			fmt.Println("READY", l.Addr())
			return s.Serve(l)
		}
		s := udp.NewServer(options.WithQBlockServer(cfg), errors,
			options.WithBlockwise(false, blockwise.SZX64, 10*time.Second),
			handler)
		l, err := coapNet.NewListenUDP("udp4", addr)
		if err != nil {
			return err
		}
		go func() { <-ctx.Done(); s.Stop() }()
		fmt.Println("READY", l.LocalAddr())
		return s.Serve(l)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := qblock.DefaultClientConfig()
	cfg.Mode = qblock.Require
	cfg.ProbingRate = 65536
	opts := []udp.Option{options.WithContext(ctx), options.WithQBlock(cfg), errors, options.WithBlockwise(false, blockwise.SZX64, 10*time.Second)}
	var cc *client.Conn
	if transport == "dtls" {
		cc, err = dtls.Dial(addr, pskConfig(), opts...)
	} else {
		cc, err = udp.Dial(addr, opts...)
	}
	if err != nil {
		return err
	}
	defer cc.Close()
	supported, err := cc.ProbeQBlock(ctx, probe)
	if err != nil {
		return fmt.Errorf("explicit capability probe: %w", err)
	}
	if !supported {
		return fmt.Errorf("peer did not confirm Q-Block support")
	}
	fmt.Println("CAPABILITY supported; selection Require")
	var resp *pool.Message
	switch method {
	case "GET":
		resp, err = cc.Get(ctx, path)
	case "POST":
		resp, err = cc.Post(ctx, path, message.TextPlain, bytes.NewReader(body))
	case "PUT":
		resp, err = cc.Put(ctx, path, message.TextPlain, bytes.NewReader(body))
	default:
		return fmt.Errorf("unsupported method %q", method)
	}
	if err != nil {
		return err
	}
	defer cc.ReleaseMessage(resp)
	if resp.Code() != codes.Content && resp.Code() != codes.Changed && resp.Code() != codes.Created {
		return fmt.Errorf("response %s", resp.Code())
	}
	body, err = io.ReadAll(resp.Body())
	if err != nil {
		return err
	}
	fmt.Printf("RESULT %s %d bytes\n", resp.Code(), len(body))
	return os.WriteFile(output, body, 0600)
}
