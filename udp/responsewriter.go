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

func (r *ResponseWriter) SetETag(value []byte) {
	r.response.SetETag(value)
}

func (r *ResponseWriter) WriteFrom(contentFormat message.MediaType, d io.ReadSeeker) error {
	r.want = true
	r.response.SetContentFormat(contentFormat)
	r.response.SetPayload(d)
	if r.response.Code() == codes.Content && !r.response.HasOption(message.ETag) {
		etag, err := message.GetETag(d)
		if err != nil {
			return err
		}
		r.response.SetOptionBytes(message.ETag, etag)
	}
	return nil
}

func (r *ResponseWriter) SetCode(code codes.Code) error {
	r.want = true
	r.response.SetCode(code)
	if code == codes.Content && r.response.Payload() != nil && !r.response.HasOption(message.ETag) {
		etag, err := message.GetETag(r.response.Payload())
		if err != nil {
			return err
		}
		r.response.SetOptionBytes(message.ETag, etag)
	}
	return nil
}

func (r *ResponseWriter) ClientConn() *ClientConn {
	return r.cc
}

func (r *ResponseWriter) wantWrite() bool {
	return r.want
}
