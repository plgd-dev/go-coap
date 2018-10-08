package coap

type getResponseWriter struct {
	w ResponseWriter
}

func (r *getResponseWriter) Write(msg Message) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return r.w.Write(msg)
}
