package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp"
)

func main() {
	co, err := udp.Dial("localhost:5685")
	if err != nil {
		log.Fatalf("Error dialing: %v", err)
	}
	defer co.Close()
	path := "/a"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := co.Get(ctx, path)
	if err != nil {
		log.Fatalf("Error sending request: %v", err)
	}
	log.Printf("Response payload: %v", resp.String())
	co.ReleaseMessage(resp)

	fetch, err := co.Fetch(ctx, "/object", message.AppJSON, bytes.NewReader([]byte(`{"field":"value"}`)))
	if err != nil {
		log.Fatalf("FETCH failed: %v", err)
	}
	if err := logResponse(co, "FETCH", fetch, codes.Content); err != nil {
		log.Fatal(err)
	}

	patch, err := co.Patch(ctx, "/object", message.AppJSONPatch,
		bytes.NewReader([]byte(`[{"op":"add","path":"/value/-","value":"world"}]`)))
	if err != nil {
		log.Fatalf("PATCH failed: %v", err)
	}
	if err := logResponse(co, "PATCH", patch, codes.Changed); err != nil {
		log.Fatal(err)
	}

	ipatch, err := co.IPatch(ctx, "/object", message.AppJSONMergePatch,
		bytes.NewReader([]byte(`{"value":["updated idempotently"]}`)))
	if err != nil {
		log.Fatalf("iPATCH failed: %v", err)
	}
	if err := logResponse(co, "iPATCH", ipatch, codes.Changed); err != nil {
		log.Fatal(err)
	}
}

func logResponse(cc interface {
	ReleaseMessage(*pool.Message)
}, method string, response *pool.Message, expectedCode codes.Code) error {
	defer cc.ReleaseMessage(response)
	if response.Code() != expectedCode {
		return fmt.Errorf("%s returned %v, want %v", method, response.Code(), expectedCode)
	}
	body, err := response.ReadBody()
	if err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}
	log.Printf("%s response: %s", method, body)
	return nil
}
