package coap

type getResponseWriter struct {
	w ResponseWriter
}

// NewResponse creates reponse for request
func (r *getResponseWriter) NewResponse(code COAPCode) Message {
	return r.w.NewResponse(code)
}

// Write send response to peer
func (r *getResponseWriter) Write(msg Message) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return r.w.Write(msg)
}
