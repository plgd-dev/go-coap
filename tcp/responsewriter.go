package tcp

import (
	"io"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
)

// A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
type ResponseWriter struct {
	want     bool
	response *Request
}

func NewResponseWriter(response *Request) *ResponseWriter {
	return &ResponseWriter{
		response: response,
	}
}

func (r *ResponseWriter) SetResponse(contentFormat message.MediaType, d io.ReadSeeker) (err error) {
	r.want = true
	r.response.SetContentFormat(contentFormat)
	return r.response.SetPayload(d)
}

func (r *ResponseWriter) SetCode(code codes.Code) {
	r.want = true
	r.response.SetCode(code)
}

func (r *ResponseWriter) wantWrite() bool {
	return r.want
}
