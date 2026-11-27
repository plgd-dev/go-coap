package requestnotsent

// Error identifies a failure that occurred before transmission.
type Error struct {
	Err error
}

func (e *Error) Error() string {
	return e.Err.Error()
}

func (e *Error) Unwrap() error {
	return e.Err
}
