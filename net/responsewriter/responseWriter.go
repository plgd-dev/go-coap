package responsewriter

import (
	"io"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/noresponse"
	"github.com/plgd-dev/go-coap/v2/message/pool"
)

// A ResponseWriter interface is used by an COAP handler to construct an COAP response.
type ResponseWriter[Client any] struct {
	noResponseValue *uint32
	response        *pool.Message
	cc              Client
}

func New[Client any](response *pool.Message, cc Client, requestOptions message.Options) *ResponseWriter[Client] {
	var noResponseValue *uint32
	v, err := requestOptions.GetUint32(message.NoResponse)
	if err == nil {
		noResponseValue = &v
	}

	return &ResponseWriter[Client]{
		response:        response,
		cc:              cc,
		noResponseValue: noResponseValue,
	}
}

func (r *ResponseWriter[Client]) SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error {
	if r.noResponseValue != nil {
		err := noresponse.IsNoResponseCode(code, *r.noResponseValue)
		if err != nil {
			return err
		}
	}

	r.response.SetCode(code)
	r.response.ResetOptionsTo(opts)
	if d != nil {
		r.response.SetContentFormat(contentFormat)
		r.response.SetBody(d)
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

func (r *ResponseWriter[Client]) SetMessage(m *pool.Message) {
	r.response = m
}

func (r *ResponseWriter[Client]) Message() *pool.Message {
	return r.response
}

func (r *ResponseWriter[Client]) ClientConn() Client {
	return r.cc
}

func (r *ResponseWriter[Client]) SendReset() {
	r.response.Reset()
	r.response.SetCode(codes.Empty)
	r.response.SetType(message.Reset)
}
