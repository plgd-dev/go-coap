package tcp

import (
	"github.com/plgd-dev/go-coap/v2/mux"
)

// WithMux set's multiplexer for handle requests.
func WithMux(m mux.Handler) HandlerFuncOpt {
	return WithHandlerFunc(mux.ToHandler[*ClientConn](m))
}
