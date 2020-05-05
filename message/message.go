package message

import (
	"context"
	"io"

	"github.com/go-ocf/go-coap/v2/message/codes"
)

// MaxTokenSize maximum of token size that can be used in message
const MaxTokenSize = 8

type Message struct {
	Context context.Context
	Token   []byte
	Code    codes.Code
	Options Options
	Body    io.ReadSeeker
}
