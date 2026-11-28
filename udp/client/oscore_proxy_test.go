package client

import (
	"context"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
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

func TestOSCOREProxyAndOuterBlockwiseAllowEDHOCOption(t *testing.T) {
	for name, setMode := range map[string]func(*Conn){
		"proxy":           func(cc *Conn) { cc.oscoreProxy = true },
		"outer blockwise": func(cc *Conn) { cc.oscoreOuterBlockwise = true },
	} {
		t.Run(name, func(t *testing.T) {
			session := newRecordingSession()
			defer session.Close()
			cfg := DefaultConfig
			conn := NewConnWithOpts(session, &cfg)
			defer conn.Close()
			setMode(conn)
			request := conn.AcquireMessage(context.Background())
			request.SetMessage(message.Message{
				Code: codes.POST,
				Type: message.NonConfirmable,
				Options: message.Options{
					{ID: message.OSCORE, Value: []byte{0x09, 0x01, 0x00}},
					{ID: message.EDHOC},
					{ID: message.Block1, Value: []byte{0x0e}},
				},
			})
			handled, err := conn.processUnknownCriticalOptions(request)
			if err != nil {
				t.Fatal(err)
			}
			if handled {
				t.Fatal("EDHOC option of an OSCORE request was rejected before OSCORE processing")
			}
			conn.ReleaseMessage(request)
		})
	}
}
