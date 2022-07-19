package tcp

import (
	"io"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/mux"
	"github.com/plgd-dev/go-coap/v2/net/responsewriter"
)

// WithMux set's multiplexer for handle requests.
func WithMux(m mux.Handler) HandlerFuncOpt {
	h := func(w *responsewriter.ResponseWriter[*ClientConn], r *pool.Message) {
		muxw := &muxResponseWriter{
			w: w,
		}
		m.ServeCOAP(muxw, &mux.Message{
			Message:     r,
			RouteParams: new(mux.RouteParams),
		})
	}
	return WithHandlerFunc(h)
}

type muxResponseWriter struct {
	w *responsewriter.ResponseWriter[*ClientConn]
}

func (w *muxResponseWriter) SetResponse(code codes.Code, contentFormat message.MediaType, d io.ReadSeeker, opts ...message.Option) error {
	return w.w.SetResponse(code, contentFormat, d, opts...)
}

func (w *muxResponseWriter) Client() mux.Client {
	return w.w.ClientConn()
}
