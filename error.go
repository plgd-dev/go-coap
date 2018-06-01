package coap

// Error errors type of coap
type Error string

func (e Error) Error() string { return string(e) }

// ErrShortRead To construct Message we need to read more data from connection
const ErrShortRead = Error("short read")
