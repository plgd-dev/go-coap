package coap

//A ResponseWriter interface is used by an CAOP handler to construct an COAP response.
//A ResponseWriter may not be used after the Handler.ServeCOAP method has returned.
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
