package coap

import (
	"context"
	"crypto/x509"
)

type Request struct {
	Msg              Message
	PeerCertificates []*x509.Certificate
	Client           *ClientConn
	Ctx              context.Context
	Sequence         uint64 // discontinuously growing number for every request from connection starts from 0
}
