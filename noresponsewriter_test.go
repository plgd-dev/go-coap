package coap

import (
	"sync"
	"testing"

	"github.com/go-ocf/go-coap/codes"
	"github.com/stretchr/testify/require"
)

func TestNoResponse2XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(2)
	exp := resp2XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponse4XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(8)
	exp := resp4XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponse5XXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(16)
	exp := resp5XXCodes
	require.Equal(t, exp, codes)
}

func TestNoResponseCombinationXXCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	codes := nr.decodeNoResponseOption(18)
	exp := append(resp2XXCodes, resp5XXCodes...)
	require.Equal(t, exp, codes)
}

func TestNoResponseAllCodes(t *testing.T) {
	nr := newNoResponseWriter(ResponseWriter(&responseWriter{req: &Request{}}))
	allCodes := nr.decodeNoResponseOption(0)
	exp := []codes.Code(nil)
	require.Equal(t, exp, allCodes)
}

func testNoResponseHandler(t *testing.T, w ResponseWriter, r *Request) {
	typ := NonConfirmable
	if r.Msg.Type() == Confirmable {
		typ = Acknowledgement
	}
	msg := r.Client.NewMessage(MessageParams{
		Type:      typ,
		Code:      codes.NotFound,
		MessageID: r.Msg.MessageID(),
		Token:     r.Msg.Token(),
	})

	err := w.WriteMsg(msg)
	if err != nil {
		if err == ErrMessageNotInterested {
			require.NoError(t, err)
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
	require.NoError(t, err)
	defer func() {
		s.Shutdown()
		<-fin
	}()

	// connect client
	c := Client{Net: "udp", Handler: func(w ResponseWriter, r *Request) {}}
	con, err := c.Dial(addr)
	require.NoError(t, err)

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
	require.NoError(t, err)
	wg.Wait()
}
