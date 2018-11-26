package coapservertcp

import (
	coap "github.com/go-ocf/go-coap/g2/message"
	coaptcp "github.com/go-ocf/go-coap/g2/message/tcp"
)

type getResponseWriter struct {
	ResponseWriter
}

// NewResponse creates reponse for request
func (r *getResponseWriter) NewResponse(code coap.COAPCode) coaptcp.TCPMessage {
	return r.ResponseWriter.NewResponse(code)
}

// Write send response to peer
func (r *getResponseWriter) WriteMsg(msg coaptcp.TCPMessage) error {
	if msg.Payload() != nil && msg.Option(ETag) == nil {
		msg.SetOption(ETag, CalcETag(msg.Payload()))
	}

	return r.ResponseWriter.WriteMsg(msg)
}
