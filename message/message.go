package message

import (
	"fmt"

	"github.com/plgd-dev/go-coap/v2/message/codes"
)

// MaxTokenSize maximum of token size that can be used in message
const MaxTokenSize = 8

type Message struct {
	Token   Token
	Options Options
	Code    codes.Code
	Payload []byte

	// For DTLS and UDP messages
	MessageID uint16
	Type      Type
}

func (r *Message) String() string {
	if r == nil {
		return "nil"
	}
	buf := fmt.Sprintf("Code: %v, Token: %v", r.Code, r.Token)
	path, err := r.Options.Path()
	if err == nil {
		buf = fmt.Sprintf("%s, Path: %v", buf, path)
	}
	cf, err := r.Options.ContentFormat()
	if err == nil {
		buf = fmt.Sprintf("%s, ContentFormat: %v", buf, cf)
	}
	queries, err := r.Options.Queries()
	if err == nil {
		buf = fmt.Sprintf("%s, Queries: %+v", buf, queries)
	}
	buf = fmt.Sprintf("%s, Type: %v, MessageID: %v", buf, r.Type, r.MessageID)
	return buf
}
