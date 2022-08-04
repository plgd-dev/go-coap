package status

import (
	"context"
	"errors"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
)

const (
	OK       codes.Code = 10000
	Timeout  codes.Code = 10001
	Canceled codes.Code = 10002
	Unknown  codes.Code = 10003
)

// Status holds error of coap
type Status struct { //nolint:errname
	err  error
	msg  *pool.Message
	code codes.Code
}

func CodeToString(c codes.Code) string {
	switch c {
	case OK:
		return "OK"
	case Timeout:
		return "Timeout"
	case Canceled:
		return "Canceled"
	case Unknown:
		return "Unknown"
	}
	return c.String()
}

func (se Status) Error() string {
	return fmt.Sprintf("coap error: code = %s desc = %v", CodeToString(se.msg.Code()), se.err)
}

// Code returns the status code contained in se.
func (se Status) Code() codes.Code {
	if se.msg != nil {
		return se.msg.Code()
	}
	return se.code
}

// Message returns a coap message.
func (se Status) Message() *pool.Message {
	return se.msg
}

// COAPError just for check interface
func (se Status) COAPError() Status {
	return se
}

// Error returns an error representing c and msg.  If c is OK, returns nil.
func Error(msg *pool.Message, err error) Status {
	return Status{
		msg: msg,
		err: err,
	}
}

// Errorf returns Error(c, fmt.Sprintf(format, a...)).
func Errorf(msg *pool.Message, format string, a ...interface{}) Status {
	return Error(msg, fmt.Errorf(format, a...))
}

// FromError returns a Status representing err if it was produced from this
// package or has a method `COAPError() Status`. Otherwise, ok is false and a
// Status is returned with codes.Unknown and the original error message.
func FromError(err error) (s Status, ok bool) {
	if err == nil {
		return Status{
			code: OK,
		}, true
	}
	var coapError interface {
		COAPError() Status
	}
	if errors.As(err, &coapError) {
		return coapError.COAPError(), true
	}
	return Status{
		code: Unknown,
		err:  err,
	}, false
}

// Convert is a convenience function which removes the need to handle the
// boolean return value from FromError.
func Convert(err error) Status {
	s, _ := FromError(err)
	return s
}

// Code returns the Code of the error if it is a Status error, codes.OK if err
// is nil, or codes.Unknown otherwise.
func Code(err error) codes.Code {
	// Don't use FromError to avoid allocation of OK status.
	if err == nil {
		return OK
	}
	var coapError interface {
		COAPError() Status
	}
	if errors.As(err, &coapError) {
		return coapError.COAPError().Code()
	}
	return Unknown
}

// FromContextError converts a context error into a Status.  It returns a
// Status with codes.OK if err is nil, or a Status with codes.Unknown if err is
// non-nil and not a context error.
func FromContextError(err error) Status {
	switch {
	case err == nil:
		return Status{
			code: OK,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return Status{
			code: Timeout,
			err:  err,
		}
	case errors.Is(err, context.Canceled):
		return Status{
			code: Canceled,
			err:  err,
		}
	default:
		return Status{
			code: Unknown,
			err:  err,
		}
	}
}
