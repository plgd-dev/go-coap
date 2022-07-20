package tcp

import (
	"github.com/plgd-dev/go-coap/v2/tcp/server"
)

func NewServer(opt ...server.ServerOption) *server.Server {
	return server.New(opt...)
}
