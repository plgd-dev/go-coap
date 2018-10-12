package coap

type getResponseWriter struct {
	ResponseWriter
}

// NewResponse creates reponse for request
func (r *getResponseWriter) NewResponse(code COAPCode) Message {
	return r.ResponseWriter.NewResponse(code)
}

// Write send response to peer
func (r *getResponseWriter) WriteMsg(msg Message) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return r.ResponseWriter.WriteMsg(msg)
}
