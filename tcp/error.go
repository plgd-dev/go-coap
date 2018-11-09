package tcpcoap

// Error errors type of coap
type Error string

func (e Error) Error() string { return string(e) }

// ErrTooSmall buffer is too small
const ErrTooSmall = Error("buffer is too small")

const ErrInvalidOptionHeaderExt = Error("invalid option header ext")
