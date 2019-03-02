package coap

import "context"

type Request struct {
	Msg    Message
	Client *ClientCommander
	Ctx    context.Context
}
