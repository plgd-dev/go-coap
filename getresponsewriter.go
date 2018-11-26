package coap

type getResponseWriter struct {
	ResponseWriter
}

// Write send response to peer
func (w *getResponseWriter) WriteMsg(msg Message) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return w.ResponseWriter.WriteMsg(msg)
}

// Write send response to peer
func (w *getResponseWriter) Write(p []byte) (n int, err error) {
	l, resp := prepareReponse(w, w.ResponseWriter.getReq().Msg.Code(), w.ResponseWriter.getCode(), w.ResponseWriter.getContentFormat(), p)
	err = w.WriteMsg(resp)
	return l, err
}
