package coap

import (
	"context"

	"github.com/go-ocf/go-coap/codes"
)

// A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
// For Obsevation (GET+option observe) it can be stored and used in another go-routine
// with using calls NewResponse, WriteContextMsg
type ResponseWriter interface {
	Write(p []byte) (n int, err error)
	// WriteContext response with payload.
	// If p is nil it writes response without payload.
	// If p is non-nil then SetContentFormat must be called before Write otherwise Write fails.
	WriteWithContext(ctx context.Context, p []byte) (n int, err error)
	// SetCode for response that is send via Write call.
	//
	// If SetCode is not called explicitly, the first call to Write
	// will trigger an implicit SetCode(Content/Changed/Deleted/Created - depends on request).
	// Thus explicit calls to SetCode are mainly used to send error codes.
	SetCode(code codes.Code)
	// SetContentFormat of payload for response that is send via Write call.
	//
	// If SetContentFormat is not called and Write is called with non-nil argumet
	// If SetContentFormat is set but Write is called with nil argument it fails
	SetContentFormat(contentFormat MediaType)

	//NewResponse create response with code and token, messageid against request
	NewResponse(code codes.Code) Message

	WriteMsg(msg Message) error
	//WriteContextMsg to client.
	//If Option ContentFormat is set and Payload is not set then call will failed.
	//If Option ContentFormat is not set and Payload is set then call will failed.
	WriteMsgWithContext(ctx context.Context, msg Message) error

	getCode() *codes.Code
	getReq() *Request
	getContentFormat() *MediaType
}

type responseWriter struct {
	req           *Request
	code          *codes.Code
	contentFormat *MediaType
}

func responseWriterFromRequest(r *Request) ResponseWriter {
	w := ResponseWriter(&responseWriter{req: r})
	switch {
	case r.Msg.Code() == codes.GET:
		switch {
		// set blockwise notice writer for observe
		case r.Client.networkSession().blockWiseEnabled() && r.Msg.Option(Observe) != nil:
			w = &blockWiseNoticeWriter{responseWriter: w}
		// set blockwise if it is enabled
		case r.Client.networkSession().blockWiseEnabled():
			w = &blockWiseResponseWriter{responseWriter: w}
		}
		w = &getResponseWriter{w}
	case r.Client.networkSession().blockWiseEnabled():
		w = &blockWiseResponseWriter{responseWriter: w}
	}
	if r.Msg.Option(NoResponse) != nil {
		w = newNoResponseWriter(w)
	}

	return w
}

// NewResponse creates reponse for request
func (r *responseWriter) NewResponse(code codes.Code) Message {
	typ := NonConfirmable
	if r.req.Msg.Type() == Confirmable {
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

// Write send response without to peer
func (r *responseWriter) WriteMsg(msg Message) error {
	return r.WriteMsgWithContext(context.Background(), msg)
}

// Write send response with context to peer
func (r *responseWriter) WriteMsgWithContext(ctx context.Context, msg Message) error {
	switch msg.Code() {
	case codes.GET, codes.POST, codes.PUT, codes.DELETE:
		return ErrInvalidReponseCode
	}
	return r.req.Client.WriteMsgWithContext(ctx, msg)
}

func prepareReponse(w ResponseWriter, reqCode codes.Code, code *codes.Code, contentFormat *MediaType, payload []byte) (int, Message) {
	respCode := codes.Content
	if code != nil {
		respCode = *code
	} else {
		switch reqCode {
		case codes.POST:
			respCode = codes.Changed
		case codes.PUT:
			respCode = codes.Created
		case codes.DELETE:
			respCode = codes.Deleted
		}
	}
	resp := w.NewResponse(respCode)
	if contentFormat != nil {
		resp.SetOption(ContentFormat, *contentFormat)
	}
	var l int
	if payload != nil {
		resp.SetPayload(payload)
		l = len(payload)
	}
	return l, resp
}

func (r *responseWriter) Write(p []byte) (n int, err error) {
	return r.WriteWithContext(context.Background(), p)
}

// Write send response to peer
func (r *responseWriter) WriteWithContext(ctx context.Context, p []byte) (n int, err error) {
	l, resp := prepareReponse(r, r.req.Msg.Code(), r.code, r.contentFormat, p)
	err = r.WriteMsgWithContext(ctx, resp)
	return l, err
}

func (r *responseWriter) SetCode(code codes.Code) {
	r.code = &code
}

func (r *responseWriter) SetContentFormat(contentFormat MediaType) {
	r.contentFormat = &contentFormat
}

func (r *responseWriter) getCode() *codes.Code {
	return r.code
}

func (r *responseWriter) getReq() *Request {
	return r.req
}

func (r *responseWriter) getContentFormat() *MediaType {
	return r.contentFormat
}
