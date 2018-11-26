package coapservertcp

import (
	coapMsg "github.com/go-ocf/go-coap/g2/message/tcp"
)

type Request struct {
	Msg    coapMsg.TCPMessage
	Client *ClientCommander
}
