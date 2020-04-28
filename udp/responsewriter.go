package udp

import (
	"io"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
)

// A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
type ResponseWriter struct {
	want     bool
	response *Message
	cc       *ClientConn
}

func NewResponseWriter(response *Message, cc *ClientConn) *ResponseWriter {
	return &ResponseWriter{
		response: response,
		cc:       cc,
	}
}

func (r *ResponseWriter) WriteFrom(contentFormat message.MediaType, d io.ReadSeeker) {
	r.want = true
	r.response.SetContentFormat(contentFormat)
	r.response.SetPayload(d)
}

func (r *ResponseWriter) SetCode(code codes.Code) {
	r.want = true
	r.response.SetCode(code)
}

func (r *ResponseWriter) ClientConn() *ClientConn {
	return r.cc
}

func (r *ResponseWriter) wantWrite() bool {
	return r.want
}
