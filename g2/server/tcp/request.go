package coapservertcp

import (
	coapTCP "github.com/go-ocf/go-coap/g2/message/tcp"
)

type Request struct {
	Msg    coapTCP.Message
	Client *ClientCommander
}
