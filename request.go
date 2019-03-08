package coap

import "context"

type Request struct {
	Msg    Message
	Client *ClientConn
	Ctx    context.Context
}
