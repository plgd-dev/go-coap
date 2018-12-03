package coap

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
	SetCode(code COAPCode)
	// SetContentFormat of payload for response that is send via Write call.
	//
	// If SetContentFormat is not called and Write is called with non-nil argumet
	// If SetContentFormat is set but Write is called with nil argument it fails
	SetContentFormat(contentFormat MediaType)

	//NewResponse create response with code and token, messageid against request
	NewResponse(code COAPCode) Message
	//WriteMsg to client.
	//If Option ContentFormat is set and Payload is not set then call will failed.
	//If Option ContentFormat is not set and Payload is set then call will failed.
	WriteMsg(Message) error

	getCode() *COAPCode
	getReq() *Request
	getContentFormat() *MediaType
}

type responseWriter struct {
	req           *Request
	code          *COAPCode
	contentFormat *MediaType
}

func responseWriterFromRequest(r *Request) ResponseWriter {
	w := ResponseWriter(&responseWriter{req: r})
	switch {
	case r.Msg.Code() == GET:
		switch {
		// set blockwise notice writer for observe
		case r.Client.networkSession.blockWiseEnabled() && r.Msg.Option(Observe) != nil:
			w = &blockWiseNoticeWriter{responseWriter: w.(*responseWriter)}
		// set blockwise if it is enabled
		case r.Client.networkSession.blockWiseEnabled():
			w = &blockWiseResponseWriter{responseWriter: w.(*responseWriter)}
		}
		w = &getResponseWriter{w}
	case r.Client.networkSession.blockWiseEnabled():
		w = &blockWiseResponseWriter{responseWriter: w.(*responseWriter)}
	}

	return w
}

// NewResponse creates reponse for request
func (r *responseWriter) NewResponse(code COAPCode) Message {
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

// Write send response to peer
func (r *responseWriter) WriteMsg(msg Message) error {
	switch msg.Code() {
	case GET, POST, PUT, DELETE:
		return ErrInvalidReponseCode
	}
	return r.req.Client.WriteMsg(msg)
}

func prepareReponse(w ResponseWriter, reqCode COAPCode, code *COAPCode, contentFormat *MediaType, payload []byte) (int, Message) {
	respCode := Content
	if code != nil {
		respCode = *code
	} else {
		switch reqCode {
		case POST:
			respCode = Changed
		case PUT:
			respCode = Created
		case DELETE:
			respCode = Deleted
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

// Write send response to peer
func (r *responseWriter) Write(p []byte) (n int, err error) {
	l, resp := prepareReponse(r, r.req.Msg.Code(), r.code, r.contentFormat, p)
	err = r.WriteMsg(resp)
	return l, err
}

func (r *responseWriter) SetCode(code COAPCode) {
	r.code = &code
}

func (r *responseWriter) SetContentFormat(contentFormat MediaType) {
	r.contentFormat = &contentFormat
}

func (r *responseWriter) getCode() *COAPCode {
	return r.code
}

func (r *responseWriter) getReq() *Request {
	return r.req
}

func (r *responseWriter) getContentFormat() *MediaType {
	return r.contentFormat
}
