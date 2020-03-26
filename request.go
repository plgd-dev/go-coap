package coap

import (
	"context"
)

type Request struct {
	Msg      Message
	Client   *ClientConn
	Ctx      context.Context
	Sequence uint64 // discontinuously growing number for every request from connection starts from 0
}
