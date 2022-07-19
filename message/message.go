package message

import (
	"fmt"
	"math"

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
	MessageID int32 // uint16 is valid, all other values are invalid, -1 is used for unset
	Type      Type  // uint8 is valid, all other values are invalid, -1 is used for unset
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
	if r.Type >= 0 && r.Type < math.MaxUint8 {
		buf = fmt.Sprintf("%s, Type: %v", buf, r.Type)
	}
	if r.MessageID >= 0 && r.MessageID < math.MaxUint16 {
		buf = fmt.Sprintf("%s, MessageID: %v", buf, r.MessageID)
	}
	if len(r.Payload) > 0 {
		buf = fmt.Sprintf("%s, PayloadLen: %v", buf, len(r.Payload))
	}
	return buf
}
