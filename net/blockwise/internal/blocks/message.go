package blocks

import (
	"context"
	"encoding/binary"
	"io"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/net/responsewriter"
	"github.com/plgd-dev/go-coap/v3/pkg/math"
)

// Client provides pooled messages to a blockwise engine.
type Client interface {
	AcquireMessage(context.Context) *pool.Message
	ReleaseMessage(*pool.Message)
}

// CloneMessage copies request metadata without retaining its body.
func CloneMessage(cc Client, r *pool.Message) *pool.Message {
	req := cc.AcquireMessage(r.Context())
	req.SetCode(r.Code())
	req.SetToken(r.Token())
	req.ResetOptionsTo(r.Options())
	req.SetType(r.Type())
	return req
}

// IsRequestCode reports whether code is a class-zero request method.
func IsRequestCode(code codes.Code) bool {
	return code >= codes.GET && code < 32
}

// WriteExchangeKeyPart writes a length-prefixed identity component.
func WriteExchangeKeyPart(h io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write(value)
}

// SendServiceUnavailable replaces the response with a 5.03 message.
func SendServiceUnavailable[C Client](cc C, w *responsewriter.ResponseWriter[C], token message.Token) {
	sendMessage := cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.ServiceUnavailable)
	sendMessage.SetToken(token)
	sendMessage.SetType(message.NonConfirmable)
	w.SetMessage(sendMessage)
}

// SendEntityTooLarge replaces the response with a 4.13 message and Size1 limit.
func SendEntityTooLarge[C Client](cc C, w *responsewriter.ResponseWriter[C], token message.Token, maxBodySize int64) {
	sendMessage := cc.AcquireMessage(w.Message().Context())
	sendMessage.SetCode(codes.RequestEntityTooLarge)
	sendMessage.SetToken(token)
	sendMessage.SetType(message.NonConfirmable)
	size1 := min(maxBodySize, int64(^uint32(0)))
	sendMessage.SetOptionUint32(message.Size1, math.CastTo[uint32](size1))
	w.SetMessage(sendMessage)
}
