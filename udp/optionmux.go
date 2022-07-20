package udp

import (
	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/udp/client"
)

// WithMux set's multiplexer for handle requests.
func WithMux(m mux.Handler) HandlerFuncOpt {
	return WithHandlerFunc(mux.ToHandler[*client.ClientConn](m))
}
