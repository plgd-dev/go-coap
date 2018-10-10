package coap

//A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
type ResponseWriter interface {
	Write(Message) error
	NewResponse(code COAPCode) Message
}

type responseWriter struct {
	req *Request
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
func (r *responseWriter) Write(msg Message) error {
	switch msg.Code() {
	case GET, POST, PUT, DELETE:
		return ErrInvalidReponseCode
	}
	return r.req.Client.Write(msg)
}
