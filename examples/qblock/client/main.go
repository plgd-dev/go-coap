package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
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
	addr := flag.String("addr", "127.0.0.1:5688", "UDP server address")
	path := flag.String("path", "/qblock", "CoAP resource path")
	method := flag.String("method", "get", "request method: get, post, or put")
	modeName := flag.String("mode", "prefer-known", "Q selection mode: prefer-known or require")
	probe := flag.Bool("probe", true, "perform explicit Q capability discovery before the request")
	bodyBytes := flag.Int("body-bytes", 8<<10, "POST/PUT request body size")
	responseBytes := flag.Int("response-bytes", 32<<10, "expected GET response body size")
	timeout := flag.Duration("timeout", 30*time.Second, "overall probe and request deadline")
	flag.Parse()

	if *bodyBytes < 0 || *responseBytes < 0 || *timeout <= 0 {
		return errors.New("body sizes must be nonnegative and timeout must be positive")
	}
	maxBody := int(qblock.DefaultManagerConfig().Transfer.MaxBodySize)
	if *bodyBytes > maxBody || *responseBytes > maxBody {
		return fmt.Errorf("body sizes must not exceed the configured default limit of %d bytes", maxBody)
	}
	mode, err := parseMode(*modeName)
	if err != nil {
		return err
	}

	config := qblock.DefaultClientConfig()
	config.Mode = mode
	conn, err := udp.Dial(*addr, options.WithQBlock(config))
	if err != nil {
		return fmt.Errorf("dial %s: %w", *addr, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("close connection: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if *probe {
		supported, err := conn.ProbeQBlock(ctx, *path)
		if err != nil {
			return fmt.Errorf("probe q-block capability: %w", err)
		}
		log.Printf("explicit q-block probe: supported=%t", supported)
	}

	var reqBody []byte
	if strings.EqualFold(*method, "post") || strings.EqualFold(*method, "put") {
		reqBody = patternedBody(*bodyBytes)
	}
	var req *pool.Message
	switch strings.ToLower(*method) {
	case "get":
		req, err = conn.NewGetRequest(ctx, *path)
	case "post":
		req, err = conn.NewPostRequest(ctx, *path, message.AppOctets, bytes.NewReader(reqBody))
	case "put":
		req, err = conn.NewPutRequest(ctx, *path, message.AppOctets, bytes.NewReader(reqBody))
	default:
		return fmt.Errorf("unsupported method %q: use get, post, or put", *method)
	}
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	defer conn.ReleaseMessage(req)

	resp, err := conn.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("request canceled or expired; a peer may already have processed it: %w", err)
		}
		return fmt.Errorf("send request: %w", err)
	}
	defer conn.ReleaseMessage(resp)

	var responseBody []byte
	if body := resp.Body(); body != nil {
		responseBody, err = io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("read response body: %w", err)
		}
	}
	if strings.EqualFold(*method, "get") {
		if !bytes.Equal(responseBody, patternedBody(*responseBytes)) {
			return fmt.Errorf("GET body mismatch: received %d bytes", len(responseBody))
		}
	} else if !bytes.Equal(responseBody, reqBody) {
		return fmt.Errorf("echo body mismatch: sent %d bytes, received %d", len(reqBody), len(responseBody))
	}

	sum := sha256.Sum256(responseBody)
	log.Printf("response: code=%s bytes=%d sha256=%x", resp.Code(), len(responseBody), sum)
	return nil
}

func parseMode(value string) (options.Mode, error) {
	switch strings.ToLower(value) {
	case "prefer-known":
		return options.PreferKnown, nil
	case "require":
		return options.Require, nil
	default:
		return 0, fmt.Errorf("invalid mode %q: use prefer-known or require", value)
	}
}

func patternedBody(size int) []byte {
	body := make([]byte, size)
	for i := range body {
		body[i] = byte((i*31 + 7) & 0xff)
	}
	return body
}
