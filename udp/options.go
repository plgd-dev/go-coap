package udp

// HandlerOpt handler option.
type HandlerOpt struct {
	h HandlerFunc
}

func (o HandlerOpt) apply(opts *serverOptions) {
	opts.handler = o.h
}

func (o HandlerOpt) applyDial(opts *dialOptions) {
	opts.handler = o.h
}

// WithHandler set handle for handling request's.
func WithHandler(h HandlerFunc) HandlerOpt {
	return HandlerOpt{h: h}
}
