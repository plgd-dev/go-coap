package coap

import (
	"reflect"
	"sync"
	"testing"

	"github.com/go-ocf/go-coap/codes"
)

func TestNoResponse2XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(2)
	exp := resp2XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponse4XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(8)
	exp := resp4XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponse5XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(16)
	exp := resp5XXCodes
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponseCombinationXXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(18)
	exp := append(resp2XXCodes, resp5XXCodes...)
	if !reflect.DeepEqual(exp, codes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, codes)
	}
}

func TestNoResponseAllCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	allCodes := nr.decodeNoResponseOption(0)
	exp := []codes.Code(nil)
	if !reflect.DeepEqual(exp, allCodes) {
		t.Fatalf("Expected\n%#v\ngot\n%#v", exp, allCodes)
	}
}

func testNoResponseHandler(t *testing.T, w ResponseWriter, r *Request) {
	msg := r.Client.NewMessage(MessageParams{
		Type:      Acknowledgement,
		Code:      codes.NotFound,
		MessageID: r.Msg.MessageID(),
		Token:     r.Msg.Token(),
	})

	err := w.WriteMsg(msg)
	if err != nil {
		if err == ErrMessageNotInterested {
			t.Fatalf("server unable to write message: %v", err)
		}
		return
	}
}

func TestNoResponseBehaviour(t *testing.T) {
	// server creation
	var wg sync.WaitGroup
	wg.Add(1)
	s, addr, fin, err := RunLocalServerUDPWithHandler("udp", ":", false, BlockWiseSzx16, func(w ResponseWriter, r *Request) {
		testNoResponseHandler(t, w, r)
		wg.Done()
	})
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	defer func() {
		s.Shutdown()
		<-fin
	}()

	// connect client
	c := Client{Net: "udp", Handler: func(w ResponseWriter, r *Request) {}}
	con, err := c.Dial(addr)
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}

	// send client request
	req := &DgramMessage{
		MessageBase: MessageBase{
			typ:  NonConfirmable,
			code: codes.GET,
		},
		messageID: 1234}

	// supressing 2XX code: example Content; No error when server sends 4XX response
	req.SetOption(NoResponse, 2)
	err = con.WriteMsg(req)
	if err != nil {
		t.Fatalf("client unable to write message: %v", err)
	}
	wg.Wait()
}
