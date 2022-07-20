package responsewriter

import (
	"io"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/noresponse"
	"github.com/plgd-dev/go-coap/v2/message/pool"
)

// A ResponseWriter is used by an COAP handler to construct an COAP response.
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

// SetResponse simplifies the setup of the response for the request. ETags must be set via options. For advanced setup, use Message().
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
	}
	return nil
}

// SetMessage replaces the response message. Ensure that Token, MessageID(udp), and Type(udp) messages are paired correctly.
func (r *ResponseWriter[Client]) SetMessage(m *pool.Message) {
	r.response = m
}

// Message direct access to the response.
func (r *ResponseWriter[Client]) Message() *pool.Message {
	return r.response
}

// ClientConn peer connection.
func (r *ResponseWriter[Client]) ClientConn() Client {
	return r.cc
}
