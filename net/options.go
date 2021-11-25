package net

// A UDPOption sets options such as heartBeat, errors parameters, etc.
type UDPOption interface {
	applyUDP(*udpConnOptions)
}

type ErrorsOpt struct {
	errors func(err error)
}

func (h ErrorsOpt) applyUDP(o *udpConnOptions) {
	o.errors = h.errors
}

func WithErrors(v func(err error)) ErrorsOpt {
	return ErrorsOpt{
		errors: v,
	}
}
