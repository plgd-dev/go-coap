package coap

import "context"

type getResponseWriter struct {
	ResponseWriter
}

// Write send response to peer
func (w *getResponseWriter) WriteContextMsg(ctx context.Context, msg Message) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return w.ResponseWriter.WriteContextMsg(ctx, msg)
}

// Write send response to peer
func (w *getResponseWriter) WriteContext(ctx context.Context, p []byte) (n int, err error) {
	l, resp := prepareReponse(w, w.ResponseWriter.getReq().Msg.Code(), w.ResponseWriter.getCode(), w.ResponseWriter.getContentFormat(), p)
	err = w.WriteContextMsg(ctx, resp)
	return l, err
}
