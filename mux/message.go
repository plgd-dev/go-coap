package mux

import (
	"context"
	"io"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
)

// Message COAP Message
type Message struct {
	Context context.Context
	Token   []byte
	Code    codes.Code
	Options message.Options
	Body    io.ReadSeeker
}
