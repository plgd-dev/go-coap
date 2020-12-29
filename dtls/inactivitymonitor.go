package dtls

import "github.com/plgd-dev/go-coap/v2/udp/client"

type InactivityMonitor interface {
	Run(cc *client.ClientConn) error
	Notify()
}
