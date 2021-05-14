package status

import (
	"context"
	"fmt"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/codes"
)

const (
	OK       codes.Code = 10000
	Timeout  codes.Code = 10001
	Canceled codes.Code = 10002
	Unknown  codes.Code = 10003
)

// Status holds error of coap
type Status struct {
	err  error
	msg  *message.Message
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
	return fmt.Sprintf("coap error: code = %s desc = %v", CodeToString(se.msg.Code), se.err)
}

// Code returns the status code contained in se.
func (se Status) Code() codes.Code {
	if se.msg != nil {
		return se.msg.Code
	}
	return se.code
}

// Message returns a coap message.
func (se Status) Message() *message.Message {
	return se.msg
}

// COAPError just for check interface
func (se Status) COAPError() Status {
	return se
}

// Error returns an error representing c and msg.  If c is OK, returns nil.
func Error(msg *message.Message, err error) Status {
	return Status{
		msg: msg,
		err: err,
	}
}

// Errorf returns Error(c, fmt.Sprintf(format, a...)).
func Errorf(msg *message.Message, format string, a ...interface{}) Status {
	return Error(msg, fmt.Errorf(format, a...))
}

// FromError returns a Status representing err if it was produced from this
// package or has a method `COAPError() *Status`. Otherwise, ok is false and a
// Status is returned with codes.Unknown and the original error message.
func FromError(err error) (s Status, ok bool) {
	if err == nil {
		return Status{
			code: OK,
		}, true
	}
	if se, ok := err.(interface {
		COAPStatus() Status
	}); ok {
		return se.COAPStatus(), true
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
	if se, ok := err.(interface {
		COAPError() Status
	}); ok {
		return se.COAPError().Code()
	}
	return Unknown
}

// FromContextError converts a context error into a Status.  It returns a
// Status with codes.OK if err is nil, or a Status with codes.Unknown if err is
// non-nil and not a context error.
func FromContextError(err error) Status {
	switch err {
	case nil:
		return Status{
			code: OK,
		}
	case context.DeadlineExceeded:
		return Status{
			code: Timeout,
			err:  err,
		}
	case context.Canceled:
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
