package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/blockwise"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	"github.com/plgd-dev/go-coap/v3/udp/client"
)

func exchange(cc *client.Conn, req *pool.Message, want codes.Code) ([]byte, error) {
	req.SetType(message.NonConfirmable)
	resp, err := cc.Do(req)
	if err != nil {
		return nil, err
	}
	defer cc.ReleaseMessage(resp)
	if resp.Code() != want {
		return nil, fmt.Errorf("unexpected response code %v, want %v", resp.Code(), want)
	}
	body, err := resp.ReadBody()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return body, nil
}

func run(ctx context.Context, addr string) error {
	var qPayloads atomic.Int64
	cc, err := udp.Dial(addr,
		options.WithContext(ctx),
		options.WithQBlockwise(options.QBlockwiseAuto),
		options.WithBlockwise(true, blockwise.SZX1024, time.Minute),
		options.WithRequestMonitor(func(_ *client.Conn, msg *pool.Message) (bool, error) {
			if msg.HasOption(message.QBlock1) || msg.HasOption(message.QBlock2) {
				qPayloads.Add(1)
			}
			return false, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer func() {
		if err := cc.Close(); err != nil {
			log.Printf("close client: %v", err)
		}
	}()

	get, err := cc.NewGetRequest(ctx, "/data")
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	defer cc.ReleaseMessage(get)
	body, err := exchange(cc, get, codes.Content)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	log.Printf("Downloaded %d bytes", len(body))

	put, err := cc.NewPutRequest(ctx, "/data", message.AppOctets, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	defer cc.ReleaseMessage(put)
	echo, err := exchange(cc, put, codes.Changed)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	if !bytes.Equal(body, echo) {
		return fmt.Errorf("upload echo differs: sent %d bytes, received %d bytes", len(body), len(echo))
	}
	log.Printf("Uploaded and verified %d bytes; received %d Q-Block messages", len(body), qPayloads.Load())
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:5688", "UDP server address")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx, *addr); err != nil {
		log.Fatal(err)
	}
}
