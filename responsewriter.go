package coap

//A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
type ResponseWriter interface {
	Write(Message) error
}

type responseWriter struct {
	req *Request
}

func (r *responseWriter) Write(msg Message) error {
	switch msg.Code() {
	case GET, POST, PUT, DELETE:
		return ErrInvalidReponseCode
	}
	return r.req.SessionNet.Write(msg)
}
