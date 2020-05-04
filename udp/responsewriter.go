package udp

import (
	"io"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/noresponse"
)

// A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
type ResponseWriter struct {
	noResponseValue *uint32
	response        *Message
	cc              *ClientConn
}

func NewResponseWriter(response *Message, cc *ClientConn, requestOptions message.Options) *ResponseWriter {
	var noResponseValue *uint32
	v, err := requestOptions.GetUint32(message.NoResponse)
	if err == nil {
		noResponseValue = &v
	}

	return &ResponseWriter{
		response:        response,
		cc:              cc,
		noResponseValue: noResponseValue,
	}
}

func (r *ResponseWriter) SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error {
	if r.noResponseValue != nil {
		err := noresponse.IsNoResponseCode(code, *r.noResponseValue)
		if err != nil {
			return err
		}
	}

	r.response.SetCode(code)
	r.response.ResetTo(opts)
	if d != nil {
		r.response.SetContentFormat(contentFormat)
		r.response.SetPayload(d)
		if !r.response.HasOption(message.ETag) {
			etag, err := message.GetETag(d)
			if err != nil {
				return err
			}
			r.response.SetOptionBytes(message.ETag, etag)
		}
	}
	return nil
}

func (r *ResponseWriter) ClientConn() *ClientConn {
	return r.cc
}
