package client

// RequestNotSentError identifies a failure that occurred before transmission.
// Transport errors must not use this wrapper if the peer might have received
// any part of the request.
type RequestNotSentError struct {
	Err error
}

func (e *RequestNotSentError) Error() string {
	return e.Err.Error()
}

func (e *RequestNotSentError) Unwrap() error {
	return e.Err
}
