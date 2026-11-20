package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

func TestOSCOREProxyAllowsOSCOREOptionToReachHandler(t *testing.T) {
	request := pool.NewMessage(context.Background())
	request.SetMessage(message.Message{
		Options: message.Options{{ID: message.OSCORE, Value: []byte{0x01}}},
	})
	conn := &Conn{oscoreProxy: true}

	handled, err := conn.processUnknownCriticalOptions(request)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("OSCORE option was rejected instead of passed to the proxy handler")
	}
}
