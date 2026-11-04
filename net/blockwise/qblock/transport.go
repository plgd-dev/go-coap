package qblock

import (
	"context"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

// Exchange routes every response token for a body until Close, rather
// than retiring a token after the first payload.
type Exchange interface {
	NewToken() (message.Token, error)
	Send(*pool.Message) error
	Receive(context.Context) (*pool.Message, error)
	Close()
}

// Transport is implemented by datagram connections. A connection owns
// one bounded deadline worker, shared by all of its Q-Block bodies.
type Transport interface {
	OpenQBlockExchange(context.Context, message.Token) (Exchange, error)
	ScheduleQBlock(string, time.Time, func() error) error
	DelayQBlock(string, time.Time)
	CancelQBlock(string)
	SendQBlock(*pool.Message) error
}

func qBodyMethod(code codes.Code) bool {
	return code == codes.POST || code == codes.PUT || code >= 5 && code <= 7
}
