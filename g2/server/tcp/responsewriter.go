package coapservertcp

import coap "github.com/go-ocf/go-coap/g2/message"
import coapMsg "github.com/go-ocf/go-coap/g2/message/tcp"

// A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
// For Obsevation (GET+option observe) it can be stored and used in another go-routine
// with using calls NewResponse, WriteMsg
type ResponseWriter interface {
	// Write response with payload.
	// If p is nil it writes response without payload.
	// If p is non-nil then SetContentFormat must be called before Write otherwise Write fails.
	Write(p []byte) (n int, err error)
	// SetCode for response that is send via Write call.
	//
	// If SetCode is not called explicitly, the first call to Write
	// will trigger an implicit SetCode(Content/Changed/Deleted/Created - depends on request).
	// Thus explicit calls to SetCode are mainly used to send error codes.
	SetCode(code coap.COAPCode)
	// SetContentFormat of payload for response that is send via Write call.
	//
	// If SetContentFormat is not called and Write is called with non-nil argumet
	// If SetContentFormat is set but Write is called with nil argument it fails
	SetContentFormat(contentFormat coap.MediaType)

	//NewResponse create response with code and token, messageid against request
	NewResponse(code coap.COAPCode) coapMsg.TCPMessage
	//WriteMsg to client.
	//If Option ContentFormat is set and Payload is not set then call will failed.
	//If Option ContentFormat is not set and Payload is set then call will failed.
	WriteMsg(coapMsg.TCPMessage) error
}

type responseWriter struct {
	req           *Request
	code          *coap.COAPCode
	contentFormat *coap.MediaType
}

// NewResponse creates reponse for request
func (r *responseWriter) NewResponse(code coap.COAPCode) coapMsg.TCPMessage {
	typ := coap.NonConfirmable
	if r.req.Msg.Type == coap.Confirmable {
		typ = Acknowledgement
	}
	resp := r.req.Client.NewMessage(MessageParams{
		Type:      typ,
		Code:      code,
		MessageID: r.req.Msg.MessageID(),
		Token:     r.req.Msg.Token(),
	})
	return resp
}

// Write send response to peer
func (r *responseWriter) WriteMsg(msg coapMsg.TCPMessage) error {
	switch msg.Code() {
	case GET, POST, PUT, DELETE:
		return ErrInvalidReponseCode
	}
	return r.req.Client.WriteMsg(msg)
}

// Write send response to peer
func (r *responseWriter) Write(p []byte) (n int, err error) {
	code := Content
	if r.code != nil {
		code = *r.code
	} else {
		switch r.req.Msg.Code() {
		case POST:
			code = Changed
		case PUT:
			code = Created
		case DELETE:
			code = Deleted
		}
	}
	resp := r.NewResponse(code)
	if r.contentFormat != nil {
		resp.SetOption(ContentFormat, *r.contentFormat)
	}
	if p != nil {
		resp.SetPayload(p)
	}
	err = r.WriteMsg(resp)
	return len(p), err
}

func (r *responseWriter) SetCode(code coap.COAPCode) {
	r.code = &code
}

func (r *responseWriter) SetContentFormat(contentFormat coap.MediaType) {
	r.contentFormat = &contentFormat
}
